package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/corpeningc/swarm/internal/agent"
	"github.com/corpeningc/swarm/internal/agent/claudecode"
	"github.com/corpeningc/swarm/internal/core"
	"github.com/corpeningc/swarm/internal/session"
	"github.com/corpeningc/swarm/internal/update"
	"github.com/corpeningc/swarm/internal/worktree"
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

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	go a.pollHooks()
}

// pollHooks is the desktop's twin of the TUI's activity tick: every second it
// sweeps Claude's hook markers (stop/notify → awaiting-input, session_start →
// the conversation id `claude --resume` needs). Without this the hooks the
// desktop writes are write-only — statuses never flip and resumes start fresh
// conversations. Ends with the Wails context on shutdown.
func (a *App) pollHooks() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-t.C:
			if a.orch.CheckHooks() {
				a.emitChange()
			}
		}
	}
}

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
	Nickname  string `json:"nickname"` // display alias; Label already prefers it
	Label     string `json:"label"`
	Repo      string `json:"repo"`
	Branch    string `json:"branch"`
	AgentName string `json:"agentName"`
	Status    string `json:"status"`
	Live      bool   `json:"live"`    // false for restored sessions (agent process gone)
	InPlace   bool   `json:"inPlace"` // runs in the repo's own working tree, not a worktree
}

func toDTO(h *session.Handle) SessionDTO {
	branch := ""
	if h.Worktree != nil {
		branch = h.Worktree.Branch
	}
	return SessionDTO{
		ID:        h.Session.ID,
		Name:      h.Session.Name,
		Nickname:  h.Session.Nickname,
		Label:     h.Session.Label(),
		Repo:      h.Session.RepoRoot,
		Branch:    branch,
		AgentName: h.Session.AgentName,
		InPlace:   h.Session.InPlace,
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

// Version returns the release this build came from ("dev" locally).
func (a *App) Version() string { return version }

// UpdateDTO is the result of an update check. Available is the only field the
// banner needs; the rest is what it shows and where it points.
type UpdateDTO struct {
	Available bool   `json:"available"`
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	URL       string `json:"url"`
}

// CheckUpdate asks GitHub whether a newer swarm has been released. Swarm has
// no auto-updater, so this is how a running app finds out. Errors (offline,
// rate-limited) are returned for the caller to swallow — a failed check must
// never be visible.
func (a *App) CheckUpdate() (*UpdateDTO, error) {
	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
	defer cancel()
	rel, err := update.Latest(ctx, update.DefaultRepo)
	if err != nil {
		return nil, err
	}
	return &UpdateDTO{
		Available: update.Newer(version, rel.Version),
		Current:   version,
		Latest:    rel.Version,
		URL:       rel.URL,
	}, nil
}

// OpenReleasePage opens a swarm release page in the system browser. Limited to
// this project's own URLs: the frontend is trusted, but a bound method that
// opens anything is a needlessly sharp edge.
func (a *App) OpenReleasePage(url string) error {
	if !strings.HasPrefix(url, "https://github.com/"+update.DefaultRepo+"/") {
		return fmt.Errorf("refusing to open %q: not a swarm release page", url)
	}
	wruntime.BrowserOpenURL(a.ctx, url)
	return nil
}

// AgentNames returns the selectable agents, default first.
func (a *App) AgentNames() []string { return a.orch.AgentNames() }

// DefaultRepo returns the repo swarm was launched in (may be "").
func (a *App) DefaultRepo() string { return a.orch.DefaultRepo() }

// mainWorkspace is the Workspaces/Conversations value standing for the
// repository's own working tree, as opposed to a swarm worktree slug ("" means
// "make a new worktree", which has no directory yet).
const mainWorkspace = "@main"

// WorkspaceDTO is one choice in the new-session workspace picker.
type WorkspaceDTO struct {
	Value string `json:"value"` // "" new worktree, "@main" the repo, else a worktree slug
	Label string `json:"label"`
	Path  string `json:"path"`
	InUse bool   `json:"inUse"` // a running session already owns it
}

// ConversationDTO is one resumable agent conversation recorded in a workspace.
// UpdatedAt is RFC3339 rather than a time.Time so the generated bindings stay
// plain JSON the frontend can hand straight to Date.
type ConversationDTO struct {
	ID        string `json:"id"`
	Summary   string `json:"summary"`
	UpdatedAt string `json:"updatedAt"`
}

// workspacePath resolves a picker value to the directory a session would run
// in. Empty for "new worktree" — that directory doesn't exist yet.
func workspacePath(repo, workspace string) string {
	switch {
	case repo == "" || workspace == "":
		return ""
	case workspace == mainWorkspace:
		return repo
	default:
		return filepath.Join(worktree.SwarmWorktreesDir(repo), filepath.FromSlash(workspace))
	}
}

// Workspaces lists where a new session can run in repo: a fresh worktree, the
// repository itself, or any worktree swarm already created there.
func (a *App) Workspaces(repo string) []WorkspaceDTO {
	if repo == "" {
		return nil
	}
	if _, err := os.Stat(repo); err != nil {
		return nil
	}
	// A worktree a running session already owns can't take a second one, so
	// the picker marks it rather than letting the spawn fail.
	busy := make(map[string]bool)
	for _, h := range a.orch.Registry().List() {
		if h.Agent != nil && h.Worktree != nil {
			busy[strings.ToLower(filepath.Clean(h.Worktree.Path))] = true
		}
	}
	inUse := func(path string) bool { return busy[strings.ToLower(filepath.Clean(path))] }

	out := []WorkspaceDTO{{
		Value: mainWorkspace, Label: "Main working tree", Path: repo, InUse: inUse(repo),
	}}
	for _, rel := range worktree.SwarmWorktreeRelPaths(repo) {
		path := workspacePath(repo, rel)
		out = append(out, WorkspaceDTO{Value: rel, Label: rel, Path: path, InUse: inUse(path)})
	}
	return out
}

// Conversations lists the agent conversations recorded in a workspace, newest
// first, so a new session can pick up where one left off. Conversations owned
// by a running session are left out — resuming one twice forks it. Empty for a
// brand-new worktree, which has no history yet.
func (a *App) Conversations(repo, workspace string) []ConversationDTO {
	path := workspacePath(repo, workspace)
	if path == "" {
		return nil
	}
	busy := make(map[string]bool)
	for _, h := range a.orch.Registry().List() {
		if h.Agent != nil && h.Session.ClaudeSessionID != "" {
			busy[h.Session.ClaudeSessionID] = true
		}
	}
	var out []ConversationDTO
	for _, c := range claudecode.Conversations(path) {
		if busy[c.ID] {
			continue
		}
		out = append(out, ConversationDTO{
			ID: c.ID, Summary: c.Summary, UpdatedAt: c.UpdatedAt.Format(time.RFC3339),
		})
	}
	return out
}

// SpawnSession creates a new session and begins streaming its output.
// workspace is a Workspaces value; resumeID continues a Conversations entry.
func (a *App) SpawnSession(repo, prompt, name, agentName, workspace, resumeID string, enableMCP bool) (*SessionDTO, error) {
	// An existing worktree is addressed by its slug, which is also the session
	// name the orchestrator reattaches by.
	if workspace != "" && workspace != mainWorkspace {
		name = workspace
	}
	h, err := a.orch.Spawn(a.ctx, core.SpawnRequest{
		Repo: repo, Prompt: prompt, Name: name, AgentName: agentName,
		EnableMCP: enableMCP, InPlace: workspace == mainWorkspace, ResumeID: resumeID,
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

// DiscardSession kills the agent and removes the session from the panel.
// removeWorktree and deleteBranch escalate the teardown; with both false the
// worktree and branch survive and a same-named session reattaches to them.
func (a *App) DiscardSession(id string, removeWorktree, deleteBranch bool) error {
	a.stopShell(id)
	err := a.orch.Discard(a.ctx, id, core.DiscardOpts{
		RemoveWorktree: removeWorktree,
		DeleteBranch:   deleteBranch,
	})
	a.mu.Lock()
	delete(a.buffers, id)
	delete(a.streaming, id)
	a.mu.Unlock()
	a.emitChange()
	return err
}

// ReorderSessions rewrites the panel order to match ids (front to back) and
// persists it, so a drag-reorder survives restarts.
func (a *App) ReorderSessions(ids []string) {
	a.orch.Registry().Reorder(ids)
	a.emitChange()
}

// SetNickname sets (or clears, with "") a session's display alias. The
// nickname never touches the branch or worktree — it's pure presentation.
func (a *App) SetNickname(id, nickname string) {
	a.orch.Registry().SetNickname(id, strings.TrimSpace(nickname))
	a.emitChange()
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
			// Activity means running: undo an awaiting-input flip from the
			// stop/notify hooks once the agent starts producing output again.
			if h, ok := a.orch.Registry().Get(id); ok && h.Session.Status == session.StatusAwaitingInput {
				a.orch.Registry().SetStatus(id, session.StatusRunning)
				a.emitChange()
			}
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
	// Drop the dead agent pointer so the DTO's live flag reflects reality and
	// the frontend's attach-to-resume path opens without an app restart. Only
	// flip active statuses — a kill already recorded StatusKilled.
	reg := a.orch.Registry()
	reg.ClearAgent(id)
	if h, ok := reg.Get(id); ok {
		if st := h.Session.Status; st == session.StatusRunning || st == session.StatusAwaitingInput {
			reg.SetStatus(id, session.StatusComplete)
		}
	}
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
