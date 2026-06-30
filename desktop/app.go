package main

import (
	"context"
	"strings"
	"sync"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/corpeningc/swarm/internal/agent"
	"github.com/corpeningc/swarm/internal/core"
	"github.com/corpeningc/swarm/internal/session"
)

// App is the Wails-bound surface. Every exported method becomes callable from
// the frontend as a promise; PTY output flows the other way as emitted events
// (see streamAgent). The struct is the thin glue — all real work lives in the
// orchestrator and the core packages it wraps.
type App struct {
	ctx  context.Context
	orch *core.Orchestrator

	mu        sync.Mutex
	buffers   map[string]*strings.Builder // full PTY output per session, for repaint
	streaming map[string]bool             // sessions with a live stream goroutine
	shells    map[string]agent.Agent      // worktree shell per session (Shell tab)
}

func NewApp(orch *core.Orchestrator) *App {
	return &App{
		orch:      orch,
		buffers:   make(map[string]*strings.Builder),
		streaming: make(map[string]bool),
		shells:    make(map[string]agent.Agent),
	}
}

func (a *App) startup(ctx context.Context) { a.ctx = ctx }

// --- event names emitted to the frontend ---

const (
	evtPTYData        = "pty:data"        // {id, data} — a chunk of agent PTY output
	evtSessionExit    = "session:exit"    // {id, code}
	evtSessionsChange = "sessions:change" // the session list changed; refetch
	evtShellData      = "shell:data"      // {id, data} — a chunk of shell PTY output
	evtShellExit      = "shell:exit"      // {id}
)

type ptyChunk struct {
	ID   string `json:"id"`
	Data string `json:"data"`
}

type exitInfo struct {
	ID   string `json:"id"`
	Code int    `json:"code"`
}

// SessionDTO is the frontend-facing shape of a session. Kept flat and
// JSON-tagged so it maps cleanly to TypeScript.
type SessionDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Label     string `json:"label"`
	Repo      string `json:"repo"`
	Branch    string `json:"branch"`
	AgentName string `json:"agentName"`
	Status    string `json:"status"`
	Live      bool   `json:"live"` // false for restored sessions (agent process gone)
}

func toDTO(h *session.Handle) SessionDTO {
	branch := ""
	if h.Worktree != nil {
		branch = h.Worktree.Branch
	}
	return SessionDTO{
		ID:        h.Session.ID,
		Name:      h.Session.Name,
		Label:     h.Session.Label(),
		Repo:      h.Session.RepoRoot,
		Branch:    branch,
		AgentName: h.Session.AgentName,
		Status:    h.Session.Status.String(),
		Live:      h.Agent != nil,
	}
}

// ListSessions returns every session in sidebar order (oldest first).
func (a *App) ListSessions() []SessionDTO {
	handles := a.orch.Registry().List()
	out := make([]SessionDTO, 0, len(handles))
	for _, h := range handles {
		out = append(out, toDTO(h))
	}
	return out
}

// AgentNames returns the selectable agents, default first.
func (a *App) AgentNames() []string { return a.orch.AgentNames() }

// DefaultRepo returns the repo swarm was launched in (may be "").
func (a *App) DefaultRepo() string { return a.orch.DefaultRepo() }

// SpawnSession creates a new session and begins streaming its output.
func (a *App) SpawnSession(repo, prompt, name, agentName string, enableMCP bool) (*SessionDTO, error) {
	h, err := a.orch.Spawn(a.ctx, core.SpawnRequest{
		Repo: repo, Prompt: prompt, Name: name, AgentName: agentName, EnableMCP: enableMCP,
	})
	if err != nil {
		return nil, err
	}
	a.startStream(h.Session.ID, h.Agent)
	a.emitChange()
	dto := toDTO(h)
	return &dto, nil
}

// ResumeSession relaunches a restored/interrupted session's agent and streams it.
func (a *App) ResumeSession(id string) (*SessionDTO, error) {
	h, err := a.orch.Resume(a.ctx, id)
	if err != nil {
		return nil, err
	}
	a.startStream(h.Session.ID, h.Agent)
	a.emitChange()
	dto := toDTO(h)
	return &dto, nil
}

// SendInput forwards raw bytes to a session's agent PTY (keystrokes, paste).
func (a *App) SendInput(id, data string) error {
	h, ok := a.orch.Registry().Get(id)
	if !ok || h.Agent == nil {
		return nil // nothing live to send to; drop silently
	}
	return h.Agent.Send(data)
}

// ResizeSession tells a session's agent PTY the new terminal dimensions.
func (a *App) ResizeSession(id string, cols, rows int) error {
	h, ok := a.orch.Registry().Get(id)
	if !ok || h.Agent == nil || cols <= 0 || rows <= 0 {
		return nil
	}
	return h.Agent.Resize(cols, rows)
}

// KillSession terminates a session's agent; the worktree stays for review.
func (a *App) KillSession(id string) error {
	err := a.orch.Kill(id)
	a.emitChange()
	return err
}

// DiscardSession kills the agent and destroys the worktree. Irreversible.
func (a *App) DiscardSession(id string) error {
	a.stopShell(id)
	err := a.orch.Discard(a.ctx, id)
	a.mu.Lock()
	delete(a.buffers, id)
	delete(a.streaming, id)
	a.mu.Unlock()
	a.emitChange()
	return err
}

// GetDiff returns the plain (uncolored) diff of a session's worktree vs its
// base ref. The frontend renders its own +/- coloring.
func (a *App) GetDiff(id string) (string, error) {
	return a.orch.Diff(a.ctx, id, false)
}

// GetBuffer returns all PTY output captured for a session so far, so the
// frontend can repaint a terminal it created late (e.g. after a window reload).
func (a *App) GetBuffer(id string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if b, ok := a.buffers[id]; ok {
		return b.String()
	}
	return ""
}

// --- shell tab ---

// OpenShell spawns (once) an interactive shell in the session's worktree and
// streams it over shell:* events. Idempotent — returns immediately if a shell
// is already running for the session.
func (a *App) OpenShell(id string) error {
	a.mu.Lock()
	if _, ok := a.shells[id]; ok {
		a.mu.Unlock()
		return nil
	}
	a.mu.Unlock()

	sh, err := a.orch.SpawnShell(a.ctx, id)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.shells[id] = sh
	a.mu.Unlock()
	go a.streamShell(id, sh)
	return nil
}

// SendShellInput forwards bytes to a session's worktree shell.
func (a *App) SendShellInput(id, data string) error {
	a.mu.Lock()
	sh := a.shells[id]
	a.mu.Unlock()
	if sh == nil {
		return nil
	}
	return sh.Send(data)
}

// ResizeShell resizes a session's worktree shell PTY.
func (a *App) ResizeShell(id string, cols, rows int) error {
	a.mu.Lock()
	sh := a.shells[id]
	a.mu.Unlock()
	if sh == nil || cols <= 0 || rows <= 0 {
		return nil
	}
	return sh.Resize(cols, rows)
}

func (a *App) stopShell(id string) {
	a.mu.Lock()
	sh := a.shells[id]
	delete(a.shells, id)
	a.mu.Unlock()
	if sh != nil {
		_ = sh.Kill()
	}
}

// --- streaming ---

// startStream launches the per-session reader goroutine if one isn't already
// running. Guards against double-streaming on resume.
func (a *App) startStream(id string, ag agent.Agent) {
	a.mu.Lock()
	if a.streaming[id] || ag == nil {
		a.mu.Unlock()
		return
	}
	a.streaming[id] = true
	if _, ok := a.buffers[id]; !ok {
		a.buffers[id] = &strings.Builder{}
	}
	a.mu.Unlock()
	go a.streamAgent(id, ag)
}

// streamAgent drains an agent's output channel, buffering every byte (for
// repaint) and emitting it to the frontend as it arrives. Exits when the
// channel closes (process gone), flipping the session to its terminal status.
func (a *App) streamAgent(id string, ag agent.Agent) {
	for ev := range ag.Output() {
		switch ev.Kind {
		case agent.EventOutput:
			a.appendBuffer(id, ev.Text)
			wruntime.EventsEmit(a.ctx, evtPTYData, ptyChunk{ID: id, Data: ev.Text})
		case agent.EventError:
			if ev.Err != nil {
				msg := "\r\n[error] " + ev.Err.Error() + "\r\n"
				a.appendBuffer(id, msg)
				wruntime.EventsEmit(a.ctx, evtPTYData, ptyChunk{ID: id, Data: msg})
			}
		case agent.EventDone:
			wruntime.EventsEmit(a.ctx, evtSessionExit, exitInfo{ID: id, Code: ev.ExitCode})
		}
	}
	a.mu.Lock()
	a.streaming[id] = false
	a.mu.Unlock()
	a.orch.Registry().SetStatus(id, session.StatusComplete)
	a.emitChange()
}

// streamShell mirrors streamAgent for the worktree shell, over shell:* events.
func (a *App) streamShell(id string, sh agent.Agent) {
	for ev := range sh.Output() {
		if ev.Kind == agent.EventOutput {
			wruntime.EventsEmit(a.ctx, evtShellData, ptyChunk{ID: id, Data: ev.Text})
		}
	}
	a.mu.Lock()
	delete(a.shells, id)
	a.mu.Unlock()
	wruntime.EventsEmit(a.ctx, evtShellExit, exitInfo{ID: id})
}

func (a *App) appendBuffer(id, text string) {
	a.mu.Lock()
	b, ok := a.buffers[id]
	if !ok {
		b = &strings.Builder{}
		a.buffers[id] = b
	}
	b.WriteString(text)
	a.mu.Unlock()
}

func (a *App) emitChange() {
	if a.ctx != nil {
		wruntime.EventsEmit(a.ctx, evtSessionsChange)
	}
}
