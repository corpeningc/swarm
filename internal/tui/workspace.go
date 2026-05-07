// Package tui implements the Bubbletea workspace UI.
package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/calebcorpening/swarm/internal/agent"
	"github.com/calebcorpening/swarm/internal/config"
	"github.com/calebcorpening/swarm/internal/session"
	"github.com/calebcorpening/swarm/internal/worktree"
)

// Mode is the input-routing state of the workspace. Only one sub-component
// receives keystrokes at a time.
type Mode int

const (
	ModeIdle Mode = iota
	ModeNewSession
	ModePicker
	// ModeAttached forwards every keystroke to the focused session's agent.
	// Detach with Ctrl+Q.
	ModeAttached
)

// AgentFactory returns a fresh adapter per session. Injected so tests and
// alternate agents (codex, aider) can swap in without touching the TUI.
type AgentFactory func() agent.Agent

// WorkspaceDeps is what cmd/swarm constructs and hands the TUI.
type WorkspaceDeps struct {
	Registry      *session.Registry
	Git           worktree.Manager
	DefaultRepo   string
	AgentFactory  AgentFactory
	PickerStartIn string // initial directory for the repo picker
}

type Workspace struct {
	deps WorkspaceDeps

	width, height int
	mode          Mode

	modal  NewSessionModal
	picker RepoPicker

	// terminals is the per-session virtual terminal that interprets the
	// agent's PTY output. Keyed by session ID.
	terminals map[string]*SessionTerminal
	focused   string

	toast      string
	toastUntil time.Time
	quitting   bool
}

func NewWorkspace(deps WorkspaceDeps) Workspace {
	return Workspace{
		deps:      deps,
		terminals: make(map[string]*SessionTerminal),
	}
}

func (w Workspace) Init() tea.Cmd { return nil }

// ----- internal messages emitted by the spawn pipeline -----

type sessionSpawnedMsg struct{ ID string }
type sessionEventMsg struct {
	ID    string
	Event agent.Event
}
type sessionDoneMsg struct{ ID string }
type spawnErrorMsg struct{ Err string }

func (w Workspace) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if sz, ok := msg.(tea.WindowSizeMsg); ok {
		w.width, w.height = sz.Width, sz.Height
		w.resizeAllSessions()
		var c1, c2 tea.Cmd
		w.modal, c1 = w.modal.Update(sz)
		w.picker, c2 = w.picker.Update(sz)
		return w, tea.Batch(c1, c2)
	}

	switch m := msg.(type) {
	case NewSessionSubmittedMsg:
		w.mode = ModeIdle
		return w, w.startSession(m.Repo, m.Prompt, m.Name)
	case NewSessionCanceledMsg:
		w.mode = ModeIdle
		return w, nil
	case BrowseRequestedMsg:
		start := w.deps.PickerStartIn
		if start == "" {
			start = filepath.Dir(w.modal.repo)
		}
		w.picker = NewRepoPicker(start)
		w.mode = ModePicker
		// Forward the cached size so the freshly-constructed filepicker
		// has a non-zero Height. Without this it renders nothing.
		var sizeCmd tea.Cmd
		if w.width > 0 && w.height > 0 {
			w.picker, sizeCmd = w.picker.Update(tea.WindowSizeMsg{Width: w.width, Height: w.height})
		}
		return w, tea.Batch(sizeCmd, w.picker.Init())
	case PickerResultMsg:
		w.modal.SetRepo(m.RepoRoot)
		w.mode = ModeNewSession
		return w, nil
	case PickerCanceledMsg:
		w.mode = ModeNewSession
		return w, nil
	case PickerErrorMsg:
		w.setToast(m.Err)
		w.mode = ModeNewSession
		return w, nil
	case sessionSpawnedMsg:
		cols, rows := w.mainPaneSize()
		w.terminals[m.ID] = NewSessionTerminal(cols, rows)
		w.focused = m.ID
		if h, ok := w.deps.Registry.Get(m.ID); ok {
			_ = h.Agent.Resize(cols, rows)
			return w, waitForEvent(m.ID, h.Agent.Output())
		}
		return w, nil
	case sessionEventMsg:
		w.applyEvent(m)
		if h, ok := w.deps.Registry.Get(m.ID); ok {
			return w, waitForEvent(m.ID, h.Agent.Output())
		}
		return w, nil
	case sessionDoneMsg:
		w.deps.Registry.SetStatus(m.ID, session.StatusComplete)
		// If we were attached to the session that just exited, drop back
		// to idle so the user can choose a new focus.
		if w.mode == ModeAttached && w.focused == m.ID {
			w.mode = ModeIdle
			w.setToast("session " + m.ID + " ended")
		}
		return w, nil
	case spawnErrorMsg:
		w.setToast(m.Err)
		return w, nil
	}

	// Route remaining messages by mode. The modal and picker need every
	// message (not just keys) so async results from their tea.Cmds — like
	// the filepicker's readDirMsg that populates the entry list — actually
	// reach them. Idle and Attached only care about keys.
	switch w.mode {
	case ModeIdle:
		if key, ok := msg.(tea.KeyMsg); ok {
			return w.handleIdleKey(key)
		}
	case ModeAttached:
		if key, ok := msg.(tea.KeyMsg); ok {
			return w.handleAttachedKey(key)
		}
	case ModeNewSession:
		var cmd tea.Cmd
		w.modal, cmd = w.modal.Update(msg)
		return w, cmd
	case ModePicker:
		var cmd tea.Cmd
		w.picker, cmd = w.picker.Update(msg)
		return w, cmd
	}

	return w, nil
}

func (w Workspace) handleIdleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "q", "ctrl+c":
		w.quitting = true
		return w, w.shutdownAndQuit()
	case "n":
		w.modal = NewSessionModalFor(w.deps.DefaultRepo)
		w.mode = ModeNewSession
		var sizeCmd tea.Cmd
		if w.width > 0 && w.height > 0 {
			w.modal, sizeCmd = w.modal.Update(tea.WindowSizeMsg{Width: w.width, Height: w.height})
		}
		return w, tea.Batch(sizeCmd, w.modal.Init())
	case "j", "down":
		w.advanceFocus(+1)
	case "k", "up":
		w.advanceFocus(-1)
	case "enter":
		if w.focused != "" {
			if _, ok := w.deps.Registry.Get(w.focused); ok {
				w.mode = ModeAttached
			}
		}
	}
	return w, nil
}

func (w Workspace) handleAttachedKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if k.Type == tea.KeyCtrlQ {
		w.mode = ModeIdle
		return w, nil
	}
	bytes := encodeKey(k)
	if len(bytes) == 0 {
		return w, nil
	}
	if w.focused == "" {
		w.mode = ModeIdle
		return w, nil
	}
	h, ok := w.deps.Registry.Get(w.focused)
	if !ok {
		w.mode = ModeIdle
		return w, nil
	}
	if err := h.Agent.Send(string(bytes)); err != nil {
		w.setToast("session ended: " + err.Error())
		w.mode = ModeIdle
	}
	return w, nil
}

func (w *Workspace) advanceFocus(delta int) {
	list := w.deps.Registry.List()
	if len(list) == 0 {
		return
	}
	idx := -1
	for i, h := range list {
		if h.Session.ID == w.focused {
			idx = i
			break
		}
	}
	idx = (idx + delta + len(list)) % len(list)
	w.focused = list[idx].Session.ID
}

func (w *Workspace) applyEvent(m sessionEventMsg) {
	term, ok := w.terminals[m.ID]
	if !ok {
		return
	}
	switch m.Event.Kind {
	case agent.EventOutput:
		term.Feed([]byte(m.Event.Text))
	case agent.EventError:
		if m.Event.Err != nil {
			term.Feed([]byte("\r\n[error] " + m.Event.Err.Error() + "\r\n"))
		}
	}
}

// mainPaneSize returns the interior dimensions of the main pane (the area
// inside the border). The virtual terminal and the agent's PTY are sized to
// this so the agent's layout matches what the user actually sees.
func (w Workspace) mainPaneSize() (cols, rows int) {
	const sidebarW = 30
	mainW := w.width - sidebarW - 4
	bodyH := w.height - 3
	return clampMin(mainW-2, 20), clampMin(bodyH-2, 5)
}

// resizeAllSessions resizes both the virtual terminal and the agent's PTY
// for every active session whenever the window size changes.
func (w *Workspace) resizeAllSessions() {
	cols, rows := w.mainPaneSize()
	for id, term := range w.terminals {
		term.Resize(cols, rows)
		if h, ok := w.deps.Registry.Get(id); ok {
			_ = h.Agent.Resize(cols, rows)
		}
	}
}

func (w *Workspace) setToast(s string) {
	w.toast = s
	w.toastUntil = time.Now().Add(4 * time.Second)
}

// ----- spawn pipeline -----

// startSession runs the heavy work (worktree create + agent spawn) off the UI
// goroutine. The result is delivered as a Bubbletea message so all state
// mutations stay single-threaded inside Update.
func (w Workspace) startSession(repo, prompt, name string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		id := w.deps.Registry.NextID()
		wt, err := w.deps.Git.Create(ctx, repo, "HEAD", id)
		if err != nil {
			return spawnErrorMsg{Err: "worktree: " + err.Error()}
		}
		a := w.deps.AgentFactory()
		opts := agent.SpawnOpts{Cwd: wt.Path, Prompt: prompt}
		if os.Getenv("SWARM_DUMP_PTY") != "" {
			dumpDir := filepath.Join(config.Home(), "dumps")
			if err := os.MkdirAll(dumpDir, 0755); err == nil {
				opts.DumpPath = filepath.Join(dumpDir, id+".log")
			}
		}
		if err := a.Spawn(context.Background(), opts); err != nil {
			_ = w.deps.Git.Destroy(context.Background(), wt)
			return spawnErrorMsg{Err: "spawn: " + err.Error()}
		}
		now := time.Now()
		h := &session.Handle{
			Session: &session.Session{
				ID: id, Name: name, RepoRoot: repo, BaseRef: "HEAD",
				Worktree: wt.Path, AgentName: "claude-code",
				Prompt: prompt, Status: session.StatusRunning,
				CreatedAt: now, UpdatedAt: now,
			},
			Worktree: wt, Agent: a,
		}
		w.deps.Registry.Add(h)
		return sessionSpawnedMsg{ID: id}
	}
}

// shutdownAndQuit kills every active agent in parallel, then emits QuitMsg
// so Bubbletea exits. Worktrees stay on disk per spec — accept/discard or
// `swarm prune` are the only paths that destroy them.
func (w Workspace) shutdownAndQuit() tea.Cmd {
	return func() tea.Msg {
		var wg sync.WaitGroup
		for _, h := range w.deps.Registry.List() {
			wg.Add(1)
			go func(a agent.Agent) {
				defer wg.Done()
				_ = a.Kill()
			}(h.Agent)
		}
		wg.Wait()
		return tea.QuitMsg{}
	}
}

// waitForEvent reads one event from the agent's output channel and converts
// it into a Bubbletea message. Update re-issues this Cmd after each event,
// forming a single-threaded streaming pipeline.
func waitForEvent(id string, ch <-chan agent.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return sessionDoneMsg{ID: id}
		}
		return sessionEventMsg{ID: id, Event: ev}
	}
}

// ----- view -----

var (
	border    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("63"))
	dim       = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	statusBar = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Background(lipgloss.Color("237")).Padding(0, 1)
	rowFocus   = lipgloss.NewStyle().Foreground(lipgloss.Color("212")).Bold(true)
	rowDim     = lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
	repoTag    = lipgloss.NewStyle().Foreground(lipgloss.Color("147"))
	toastBox   = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Padding(0, 1)
	attachTag  = lipgloss.NewStyle().Foreground(lipgloss.Color("16")).Background(lipgloss.Color("82")).Bold(true).Padding(0, 1)
)

func (w Workspace) View() string {
	if w.quitting {
		return ""
	}
	if w.width == 0 {
		return "starting…"
	}

	body := w.renderBody()
	status := statusBar.Width(w.width).Render(w.renderStatus())
	view := lipgloss.JoinVertical(lipgloss.Left, body, status)

	switch w.mode {
	case ModeNewSession:
		return lipgloss.Place(w.width, w.height, lipgloss.Center, lipgloss.Center, w.modal.View())
	case ModePicker:
		return lipgloss.Place(w.width, w.height, lipgloss.Center, lipgloss.Center, w.picker.View())
	}
	return view
}

func (w Workspace) renderBody() string {
	sidebarW := 30
	mainW := w.width - sidebarW - 4
	bodyH := w.height - 3

	sidebar := border.Width(sidebarW).Height(bodyH).Render(w.renderSidebar(sidebarW))
	main := border.Width(mainW).Height(bodyH).Render(w.renderMain(mainW, bodyH-2))
	return lipgloss.JoinHorizontal(lipgloss.Top, sidebar, main)
}

func (w Workspace) renderSidebar(width int) string {
	list := w.deps.Registry.List()
	if len(list) == 0 {
		return "Sessions\n" +
			dim.Render(strings.Repeat("─", width-2)) + "\n" +
			dim.Render("(no sessions)\npress n to spawn")
	}
	rows := []string{"Sessions", dim.Render(strings.Repeat("─", width-2))}
	for _, h := range list {
		row := fmt.Sprintf("%s %s", h.Session.Label(), h.Session.Status)
		if name := filepath.Base(h.Session.RepoRoot); name != "" {
			row += " " + repoTag.Render("· "+name)
		}
		if h.Session.ID == w.focused {
			rows = append(rows, rowFocus.Render("▎ "+row))
		} else {
			rows = append(rows, rowDim.Render("  "+row))
		}
	}
	return strings.Join(rows, "\n")
}

func (w Workspace) renderMain(_, height int) string {
	if w.focused == "" {
		return "Swarm v0.0.1-dev\n\n" +
			dim.Render("welcome. press n to spawn a session.\n\n"+
				"q quit · n new · j/k navigate · enter attach")
	}
	term, ok := w.terminals[w.focused]
	if !ok {
		return dim.Render("waiting for output…")
	}
	out := term.Render()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) > height {
		lines = lines[len(lines)-height:]
	}
	return strings.Join(lines, "\n")
}

func (w Workspace) renderStatus() string {
	var head string
	if w.mode == ModeAttached {
		head = attachTag.Render("ATTACHED · ctrl+q to detach") + "  "
	}
	parts := []string{fmt.Sprintf("%d sessions", w.deps.Registry.Len())}
	if w.focused != "" {
		label := w.focused
		if h, ok := w.deps.Registry.Get(w.focused); ok {
			label = h.Session.Label()
		}
		parts = append(parts, "focus="+label)
	}
	parts = append(parts, fmt.Sprintf("%dx%d", w.width, w.height))
	left := head + strings.Join(parts, " · ")
	if w.toast != "" && time.Now().Before(w.toastUntil) {
		return left + "  " + toastBox.Render("⚠ "+w.toast)
	}
	return left
}
