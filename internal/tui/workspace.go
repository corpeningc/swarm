// Package tui implements the Bubbletea workspace UI.
package tui

import (
	"context"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/corpeningc/swarm/internal/agent"
	"github.com/corpeningc/swarm/internal/config"
	"github.com/corpeningc/swarm/internal/memory"
	"github.com/corpeningc/swarm/internal/session"
	"github.com/corpeningc/swarm/internal/worktree"
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
	// ModeConfirm shows a yes/no prompt for destructive operations
	// (discard). y confirms, n/esc cancels.
	ModeConfirm
	// ModeMemory shows the project-memory editor for a repo.
	ModeMemory
)

// ViewMode picks what the main pane renders for the focused session —
// the live virtual terminal output, or a snapshot of the worktree's
// git diff against its base ref.
type ViewMode int

const (
	ViewLive ViewMode = iota
	ViewDiff
	// ViewShell shows an interactive shell running in the session's worktree
	// — the integration surface for git ops (commit, push, gh pr create).
	ViewShell
)

// AgentFactory returns a fresh adapter per session. Injected so tests and
// alternate agents (codex, aider) can swap in without touching the TUI.
type AgentFactory func() agent.Agent

// WorkspaceDeps is what cmd/swarm constructs and hands the TUI.
type WorkspaceDeps struct {
	Registry    *session.Registry
	Git         worktree.Manager
	DefaultRepo string
	// AgentFactories maps an agent name (claude, codex, aider) to its
	// constructor; AgentNames is the display/selection order with [0] as the
	// default.
	AgentFactories map[string]AgentFactory
	AgentNames     []string
	ShellFactory   AgentFactory // per-session interactive shell for the Shell tab
	PickerStartIn  string       // initial directory for the repo picker
}

// agentFactory returns the constructor for the named agent, falling back to
// the default (AgentNames[0]) when the name is unknown or empty.
func (d WorkspaceDeps) agentFactory(name string) AgentFactory {
	if f, ok := d.AgentFactories[name]; ok {
		return f
	}
	if len(d.AgentNames) > 0 {
		return d.AgentFactories[d.AgentNames[0]]
	}
	return nil
}

type Workspace struct {
	deps WorkspaceDeps

	width, height int
	mode          Mode

	modal    NewSessionModal
	picker   RepoPicker
	memModal MemoryModal

	// terminals is the per-session virtual terminal that interprets the
	// agent's PTY output. Keyed by session ID.
	terminals map[string]*SessionTerminal
	focused   string

	// shells / shellTerminals hold the lazily-spawned Shell-tab process and
	// its virtual terminal, keyed by session ID. Spawned on first switch to
	// the Shell tab; respawnable after the shell exits.
	shells         map[string]agent.Agent
	shellTerminals map[string]*SessionTerminal

	// viewMode picks what the main pane shows for the focused session.
	viewMode ViewMode
	// diffSnapshots holds the most-recently-fetched parsed diff per
	// session, with the user's keep/discard selection. Refreshed on
	// tab-toggle or 'r'.
	diffSnapshots map[string]*DiffSnapshot

	// lastActivity is the wall-clock time of the most recent agent event
	// for each session, used to flip running ↔ awaiting-input.
	lastActivity map[string]time.Time

	// diffStats holds the most-recent +added/-deleted line counts per
	// session, shown in the sidebar. Refreshed on a slow tick.
	diffStats map[string]diffStat

	toast      string
	toastUntil time.Time

	// confirmPrompt is rendered to the user while in ModeConfirm.
	// pendingAction runs when the user presses 'y'.
	confirmPrompt string
	pendingAction func() tea.Cmd

	// attachOnSpawn holds a session ID that should switch to ModeAttached as
	// soon as its sessionSpawnedMsg arrives — set when Enter resumes a
	// restored session so the user lands inside it without a second keypress.
	attachOnSpawn string

	quitting bool
}

func NewWorkspace(deps WorkspaceDeps) Workspace {
	return Workspace{
		deps:           deps,
		terminals:      make(map[string]*SessionTerminal),
		shells:         make(map[string]agent.Agent),
		shellTerminals: make(map[string]*SessionTerminal),
		diffSnapshots:  make(map[string]*DiffSnapshot),
		lastActivity:   make(map[string]time.Time),
		diffStats:      make(map[string]diffStat),
	}
}

// diffStat is the +added/-deleted line summary for a session's worktree.
type diffStat struct{ add, del int }

// activityTickInterval is how often we poll for "session idle long enough
// to flip to awaiting-input".
const activityTickInterval = 1 * time.Second

// awaitingInputThreshold is the silence-based FALLBACK for flipping a running
// session to awaiting-input. The primary, authoritative signal is Claude's
// Stop hook (see checkHooks), which fires the instant a turn ends. We keep a
// long silence timeout only to catch agents whose hooks didn't fire — short
// enough to eventually notice, long enough not to false-flip an agent that's
// merely thinking mid-turn.
const awaitingInputThreshold = 25 * time.Second

// activityTickMsg fires on the activityTickInterval to re-evaluate every
// running session's running ↔ awaiting-input flag.
type activityTickMsg struct{}

func tickActivity() tea.Cmd {
	return tea.Tick(activityTickInterval, func(time.Time) tea.Msg {
		return activityTickMsg{}
	})
}

// statsTickInterval is how often we recompute per-session diff stats for the
// sidebar. Slower than the activity tick — it shells out to git per session,
// and refreshStats further limits which sessions it touches each tick.
const statsTickInterval = 4 * time.Second

type statsTickMsg struct{}
type statsRefreshedMsg struct{ stats map[string]diffStat }

func tickStats() tea.Cmd {
	return tea.Tick(statsTickInterval, func(time.Time) tea.Msg {
		return statsTickMsg{}
	})
}

func (w Workspace) Init() tea.Cmd { return tea.Batch(tickActivity(), tickStats()) }

// ----- internal messages emitted by the spawn pipeline -----

type sessionSpawnedMsg struct{ ID string }
type sessionEventMsg struct {
	ID    string
	Event agent.Event
}
type sessionDoneMsg struct{ ID string }
type spawnErrorMsg struct{ Err string }

// sessionStatusMsg signals that a session's status changed (without
// removing it). Workspace just needs to redraw the sidebar.
type sessionStatusMsg struct{ ID string }

// sessionRemovedMsg signals that a session is gone — discarded. Workspace
// cleans up its terminal map and focus. Warn, if set, is surfaced as a toast
// (e.g. the worktree dir couldn't be fully deleted but we removed the session
// anyway so the user isn't stuck).
type sessionRemovedMsg struct {
	ID   string
	Warn string
}

// diffRefreshedMsg carries a freshly-parsed diff snapshot for a session.
type diffRefreshedMsg struct {
	ID       string
	Snapshot *DiffSnapshot
	Err      string
}

// shell lifecycle messages, mirroring the agent ones but for the Shell tab's
// per-session shell process.
type shellSpawnedMsg struct {
	ID    string
	Shell agent.Agent
}
type shellEventMsg struct {
	ID    string
	Event agent.Event
}
type shellDoneMsg struct{ ID string }

func (w Workspace) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if sz, ok := msg.(tea.WindowSizeMsg); ok {
		w.width, w.height = sz.Width, sz.Height
		w.resizeAllSessions()
		var c1, c2, c3 tea.Cmd
		w.modal, c1 = w.modal.Update(sz)
		w.picker, c2 = w.picker.Update(sz)
		if w.mode == ModeMemory {
			w.memModal, c3 = w.memModal.Update(sz)
		}
		return w, tea.Batch(c1, c2, c3)
	}

	if mouse, ok := msg.(tea.MouseMsg); ok {
		return w.handleMouse(mouse)
	}

	switch m := msg.(type) {
	case NewSessionSubmittedMsg:
		w.mode = ModeIdle
		return w, w.startSession(m.Repo, m.Prompt, m.Name, m.Agent, m.EnableMCP)
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
	case MemorySavedMsg:
		w.mode = ModeIdle
		w.setToast("memory saved")
		return w, nil
	case MemoryCanceledMsg:
		w.mode = ModeIdle
		return w, nil
	case PickerErrorMsg:
		w.setToast(m.Err)
		w.mode = ModeNewSession
		return w, nil
	case sessionSpawnedMsg:
		cols, rows := w.mainPaneSize()
		w.terminals[m.ID] = NewSessionTerminal(cols, rows)
		w.lastActivity[m.ID] = time.Now()
		w.focused = m.ID
		// Auto-attach when this spawn was a resume triggered by Enter on a
		// restored session.
		if w.attachOnSpawn == m.ID {
			w.mode = ModeAttached
			w.viewMode = ViewLive
			w.attachOnSpawn = ""
		}
		if h, ok := w.deps.Registry.Get(m.ID); ok {
			_ = h.Agent.Resize(cols, rows)
			return w, waitForEvent(m.ID, h.Agent.Output())
		}
		return w, nil
	case sessionEventMsg:
		w.applyEvent(m)
		w.lastActivity[m.ID] = time.Now()
		// Activity always means running. If we'd flipped to
		// awaiting-input during a quiet stretch, flip back.
		if h, ok := w.deps.Registry.Get(m.ID); ok {
			if h.Session.Status == session.StatusAwaitingInput {
				w.deps.Registry.SetStatus(m.ID, session.StatusRunning)
			}
			return w, waitForEvent(m.ID, h.Agent.Output())
		}
		return w, nil
	case activityTickMsg:
		before := w.awaitingCount()
		w.checkHooks()
		w.evaluateAwaitingInput()
		cmds := []tea.Cmd{tickActivity(), w.titleCmd()}
		// Ring once when the awaiting set grows — a session newly needs you.
		if w.awaitingCount() > before {
			cmds = append(cmds, bell())
		}
		return w, tea.Batch(cmds...)
	case statsTickMsg:
		return w, tea.Batch(w.refreshStats(), tickStats())
	case statsRefreshedMsg:
		// Merge — refreshStats only recomputes a subset, so keep the cached
		// stats for sessions it skipped this tick.
		maps.Copy(w.diffStats, m.stats)
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
		w.attachOnSpawn = "" // a resume may have failed; don't latch the intent
		return w, nil
	case sessionStatusMsg:
		// No-op other than triggering a redraw via the message round-trip.
		return w, nil
	case sessionRemovedMsg:
		if m.Warn != "" {
			w.setToast(m.Warn)
		}
		delete(w.terminals, m.ID)
		delete(w.shells, m.ID)
		delete(w.shellTerminals, m.ID)
		delete(w.diffSnapshots, m.ID)
		// Clean up the per-session hooks dir if we can find which repo
		// the session lived in. Best-effort: registry lookup races with
		// the removal we just received, so fall back to default repo.
		repo := w.deps.DefaultRepo
		if h, ok := w.deps.Registry.Get(m.ID); ok && h.Worktree != nil {
			repo = h.Worktree.RepoRoot
		}
		if repo != "" {
			_ = os.RemoveAll(filepath.Join(repo, ".swarm", "hooks", m.ID))
		}
		if w.focused == m.ID {
			// Pick another session if any remain.
			list := w.deps.Registry.List()
			if len(list) > 0 {
				w.focused = list[0].Session.ID
			} else {
				w.focused = ""
			}
		}
		return w, nil
	case shellSpawnedMsg:
		w.shells[m.ID] = m.Shell
		cols, rows := w.mainPaneSize()
		w.shellTerminals[m.ID] = NewSessionTerminal(cols, rows)
		_ = m.Shell.Resize(cols, rows)
		return w, waitForShellEvent(m.ID, m.Shell.Output())
	case shellEventMsg:
		if term, ok := w.shellTerminals[m.ID]; ok {
			term.Feed([]byte(m.Event.Text))
		}
		if sh, ok := w.shells[m.ID]; ok {
			return w, waitForShellEvent(m.ID, sh.Output())
		}
		return w, nil
	case shellDoneMsg:
		delete(w.shells, m.ID)
		delete(w.shellTerminals, m.ID)
		// If we were attached to this session's shell, drop back to idle so
		// the next Shell-tab switch spawns a fresh shell.
		if w.focused == m.ID && w.viewMode == ViewShell {
			if w.mode == ModeAttached {
				w.mode = ModeIdle
			}
			w.viewMode = ViewLive
		}
		return w, nil
	case diffRefreshedMsg:
		if m.Err != "" {
			w.setToast(m.Err)
		} else {
			// If the user already had a snapshot with selection state,
			// preserve it across refresh by carrying over Keep flags
			// for any path that's still present.
			if prev, ok := w.diffSnapshots[m.ID]; ok && prev != nil {
				kept := make(map[string]bool, len(prev.Files))
				for _, f := range prev.Files {
					kept[f.Path] = f.Keep
				}
				for _, f := range m.Snapshot.Files {
					if v, ok := kept[f.Path]; ok {
						f.Keep = v
					}
				}
			}
			w.diffSnapshots[m.ID] = m.Snapshot
		}
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
	case ModeConfirm:
		if key, ok := msg.(tea.KeyMsg); ok {
			return w.handleConfirmKey(key)
		}
	case ModeNewSession:
		var cmd tea.Cmd
		w.modal, cmd = w.modal.Update(msg)
		return w, cmd
	case ModePicker:
		var cmd tea.Cmd
		w.picker, cmd = w.picker.Update(msg)
		return w, cmd
	case ModeMemory:
		var cmd tea.Cmd
		w.memModal, cmd = w.memModal.Update(msg)
		return w, cmd
	}

	return w, nil
}

func (w Workspace) handleIdleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Diff view repurposes j/k/space for file navigation.
	if w.viewMode == ViewDiff {
		if model, cmd, handled := w.handleDiffKey(k); handled {
			return model, cmd
		}
	}
	switch k.String() {
	case "q":
		w.confirmPrompt = "quit swarm? running agents will be killed"
		w.pendingAction = func() tea.Cmd {
			w.quitting = true
			return w.shutdownAndQuit()
		}
		w.mode = ModeConfirm
	case "ctrl+c":
		// Ctrl+C is the terminal-native abort, no confirmation.
		w.quitting = true
		return w, w.shutdownAndQuit()
	case "n":
		w.modal = NewSessionModalFor(w.deps.DefaultRepo, w.deps.AgentNames)
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
		if h, ok := w.focusedHandle(); ok {
			if w.viewMode == ViewShell {
				// Attach to the worktree shell — spawn it if the user hit
				// enter before it came up. Works for restored sessions too,
				// since the shell only needs the worktree, not a live agent.
				w.mode = ModeAttached
				return w, w.ensureShell(h)
			}
			if h.Agent == nil {
				// Restored/interrupted session: relaunch its agent in the
				// existing worktree (resuming the Claude conversation if we
				// captured its id) and attach as soon as it's up.
				w.attachOnSpawn = h.Session.ID
				w.setToast("resuming " + h.Session.Label() + "…")
				return w, w.resumeSession(h)
			}
			w.mode = ModeAttached
		}
	case "x":
		// Kill is recoverable — agent dies, worktree stays for review or
		// later discard. No confirmation.
		if h, ok := w.focusedHandle(); ok {
			if h.Agent == nil {
				// Restored session — no live process. Just mark killed.
				w.deps.Registry.SetStatus(h.Session.ID, session.StatusKilled)
			} else {
				return w, w.killSession(h)
			}
		}
	case "d":
		// Discard destroys the worktree (and any uncommitted changes
		// inside it). Always confirm.
		if h, ok := w.focusedHandle(); ok {
			id := h.Session.Label()
			w.confirmPrompt = "discard " + id + "? worktree will be destroyed"
			w.pendingAction = func() tea.Cmd { return w.discardSession(h) }
			w.mode = ModeConfirm
		}
	case "m":
		// Edit the focused session's repo memory (falls back to default repo).
		repo := w.deps.DefaultRepo
		if h, ok := w.focusedHandle(); ok && h.Session.RepoRoot != "" {
			repo = h.Session.RepoRoot
		}
		if repo == "" {
			w.setToast("no repo to edit memory for")
			return w, nil
		}
		w.memModal = NewMemoryModal(repo)
		w.mode = ModeMemory
		var sizeCmd tea.Cmd
		if w.width > 0 && w.height > 0 {
			w.memModal, sizeCmd = w.memModal.Update(tea.WindowSizeMsg{Width: w.width, Height: w.height})
		}
		return w, tea.Batch(sizeCmd, w.memModal.Init())
	case "tab":
		// Tab cycles the main pane: Preview → Diff → Shell → Preview.
		// Entering Diff refreshes the snapshot; entering Shell spawns the
		// worktree shell on first use.
		return w, w.cycleView()
	case "r":
		// Refresh the cached diff for the focused session.
		if w.viewMode == ViewDiff {
			if h, ok := w.focusedHandle(); ok {
				return w, w.refreshDiff(h)
			}
		}
	}
	return w, nil
}

// handleDiffKey is the diff-view-specific keymap. The diff is read-only
// review now that integration happens in the Shell tab — j/k just navigate
// files. Returns (model, cmd, handled): handled=false means handleIdleKey
// should continue with its session-level keymap.
func (w Workspace) handleDiffKey(k tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	snap := w.diffSnapshots[w.focused]
	if snap == nil || len(snap.Files) == 0 {
		return w, nil, false
	}
	switch k.String() {
	case "j", "down":
		snap.Cursor = (snap.Cursor + 1) % len(snap.Files)
		snap.ScrollY, snap.ScrollX = 0, 0 // new file starts at the top-left
		return w, nil, true
	case "k", "up":
		snap.Cursor = (snap.Cursor - 1 + len(snap.Files)) % len(snap.Files)
		snap.ScrollY, snap.ScrollX = 0, 0
		return w, nil, true
	case "ctrl+d", "pgdown":
		snap.ScrollY += 10
		return w, nil, true
	case "ctrl+u", "pgup":
		snap.ScrollY = max(snap.ScrollY-10, 0)
		return w, nil, true
	case "l", "right":
		snap.ScrollX += 10
		return w, nil, true
	case "h", "left":
		snap.ScrollX = max(snap.ScrollX-10, 0)
		return w, nil, true
	}
	return w, nil, false
}

// cycleView advances the main-pane tab: Preview → Diff → Shell → Preview.
// Returns any Cmd needed to populate the entered view (diff refresh / shell
// spawn).
func (w *Workspace) cycleView() tea.Cmd {
	h, ok := w.focusedHandle()
	if !ok {
		return nil
	}
	switch w.viewMode {
	case ViewLive:
		w.viewMode = ViewDiff
		return w.refreshDiff(h)
	case ViewDiff:
		w.viewMode = ViewShell
		return w.ensureShell(h)
	default:
		w.viewMode = ViewLive
		return nil
	}
}

// ensureShell spawns the worktree shell for a session if one isn't already
// running. Returns nil when the shell already exists. The spawn runs off the
// UI goroutine and reports back via shellSpawnedMsg.
func (w Workspace) ensureShell(h *session.Handle) tea.Cmd {
	id := h.Session.ID
	if _, ok := w.shells[id]; ok {
		return nil
	}
	if h.Worktree == nil {
		return func() tea.Msg { return spawnErrorMsg{Err: "shell: session has no worktree"} }
	}
	cwd := h.Worktree.Path
	factory := w.deps.ShellFactory
	return func() tea.Msg {
		sh := factory()
		if err := sh.Spawn(context.Background(), agent.SpawnOpts{Cwd: cwd}); err != nil {
			return spawnErrorMsg{Err: "shell: " + err.Error()}
		}
		return shellSpawnedMsg{ID: id, Shell: sh}
	}
}

// waitForShellEvent is waitForEvent's Shell-tab twin: it streams the shell's
// PTY output into shellEventMsg, closing into shellDoneMsg on exit.
func waitForShellEvent(id string, ch <-chan agent.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return shellDoneMsg{ID: id}
		}
		return shellEventMsg{ID: id, Event: ev}
	}
}

// focusedHandle returns the registered Handle for the currently focused
// session, or (nil, false) if there is no focus or the focus is stale.
func (w Workspace) focusedHandle() (*session.Handle, bool) {
	if w.focused == "" {
		return nil, false
	}
	return w.deps.Registry.Get(w.focused)
}

// handleMouse forwards scroll-wheel events. In the Diff tab it scrolls our
// own buffer; in Preview/Shell it forwards an xterm wheel sequence to the
// backing process so it scrolls its own view — but only when that process has
// enabled mouse reporting, so a bare shell doesn't receive escape-byte
// garbage. Clicks/motion are ignored (pane-relative coordinate translation is
// fragile and unnecessary for scrolling). Works whether or not attached.
func (w Workspace) handleMouse(m tea.MouseMsg) (tea.Model, tea.Cmd) {
	if w.mode != ModeIdle && w.mode != ModeAttached {
		return w, nil // modals/pickers don't scroll panes
	}
	up := m.Button == tea.MouseButtonWheelUp
	down := m.Button == tea.MouseButtonWheelDown
	if (!up && !down) || w.focused == "" {
		return w, nil
	}

	if w.viewMode == ViewDiff {
		if snap := w.diffSnapshots[w.focused]; snap != nil {
			if up {
				snap.ScrollY = max(snap.ScrollY-3, 0)
			} else {
				snap.ScrollY += 3
			}
		}
		return w, nil
	}

	var term *SessionTerminal
	var target agent.Agent
	if w.viewMode == ViewShell {
		term, target = w.shellTerminals[w.focused], w.shells[w.focused]
	} else if h, ok := w.deps.Registry.Get(w.focused); ok {
		term, target = w.terminals[w.focused], h.Agent
	}
	if term == nil || target == nil {
		return w, nil
	}
	enabled, sgr := term.MouseMode()
	if !enabled {
		return w, nil
	}
	// Translate absolute screen coords into approximate pane-relative cells
	// (sidebar is 30 wide + border; tab bar + border above). Exactness
	// doesn't matter for wheel events.
	_ = target.Send(string(encodeWheel(up, sgr, m.X-32, m.Y-2)))
	return w, nil
}

func (w Workspace) handleConfirmKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "y", "Y":
		action := w.pendingAction
		w.pendingAction = nil
		w.confirmPrompt = ""
		w.mode = ModeIdle
		if action != nil {
			return w, action()
		}
		return w, nil
	case "n", "N", "esc":
		w.pendingAction = nil
		w.confirmPrompt = ""
		w.mode = ModeIdle
		return w, nil
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
	// Route to the shell when the Shell tab is active, else to the agent.
	var target agent.Agent
	if w.viewMode == ViewShell {
		target = w.shells[w.focused]
	} else if h, ok := w.deps.Registry.Get(w.focused); ok {
		target = h.Agent
	}
	if target == nil {
		// Nothing live to send to (e.g. shell still spawning, or a restored
		// session with no agent). Stay attached; keystrokes are dropped.
		return w, nil
	}
	if err := target.Send(string(bytes)); err != nil {
		w.setToast("session ended: " + err.Error())
		w.mode = ModeIdle
	}
	return w, nil
}

func (w *Workspace) advanceFocus(delta int) {
	list := w.displayList()
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

// displayList is the registry's sessions in sidebar/navigation order:
// awaiting-input sessions float to the top so the ones that need the user are
// always reachable first; the rest keep registry order. Stable so focus
// doesn't jump around between ticks.
func (w Workspace) displayList() []*session.Handle {
	list := w.deps.Registry.List()
	sort.SliceStable(list, func(i, j int) bool {
		ai := list[i].Session.Status == session.StatusAwaitingInput
		aj := list[j].Session.Status == session.StatusAwaitingInput
		return ai && !aj
	})
	return list
}

// awaitingCount reports how many sessions are waiting on the user — surfaced
// in the window title and used to decide when to ring the attention bell.
func (w Workspace) awaitingCount() int {
	n := 0
	for _, h := range w.deps.Registry.List() {
		if h.Session.Status == session.StatusAwaitingInput {
			n++
		}
	}
	return n
}

// titleCmd sets the terminal window title to reflect how many sessions need
// attention, so swarm is useful even when it's not the focused window.
func (w Workspace) titleCmd() tea.Cmd {
	if n := w.awaitingCount(); n > 0 {
		return tea.SetWindowTitle(fmt.Sprintf("swarm — %d awaiting", n))
	}
	return tea.SetWindowTitle("swarm")
}

// bell writes a BEL to the controlling terminal as an attention nudge when a
// session transitions to awaiting-input. BEL doesn't move the cursor, so it's
// safe to emit alongside the alt-screen Bubbletea owns.
func bell() tea.Cmd {
	return func() tea.Msg {
		fmt.Fprint(os.Stderr, "\a")
		return nil
	}
}

func (w *Workspace) applyEvent(m sessionEventMsg) {
	term, ok := w.terminals[m.ID]
	if !ok {
		return
	}
	switch m.Event.Kind {
	case agent.EventOutput:
		term.Feed([]byte(m.Event.Text))
		w.captureClaudeResume(m.ID, m.Event.Text)
	case agent.EventError:
		if m.Event.Err != nil {
			term.Feed([]byte("\r\n[error] " + m.Event.Err.Error() + "\r\n"))
		}
	}
}

// claudeResumePattern matches the line Claude Code prints near session
// end: "Resume this session with: claude --resume <uuid>". Captures the
// uuid for replay on the next reattach into the same worktree.
var claudeResumePattern = regexp.MustCompile(`claude --resume ([0-9a-f-]{8,})`)

func (w *Workspace) captureClaudeResume(id, text string) {
	h, ok := w.deps.Registry.Get(id)
	if !ok || h.Session.ClaudeSessionID != "" {
		return
	}
	match := claudeResumePattern.FindStringSubmatch(text)
	if len(match) < 2 {
		return
	}
	h.Session.ClaudeSessionID = match[1]
	// Triggers persist via SetStatus' write path. We could add a more
	// specific helper but reusing the existing one keeps the API small.
	w.deps.Registry.SetStatus(id, h.Session.Status)
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

// checkHooks polls each session's marker dir for files dropped by Claude
// Code's hook system:
//
//   - stop / notify: presence = agent paused for input. Flip status to
//     awaiting and remove the marker.
//   - session_start: contains the full JSON payload Claude piped to the
//     hook, including the session_id we want to use for `--resume` later.
//     Read once, persist to the session, leave the file in place as a
//     cache (rewrites are idempotent).
func (w *Workspace) checkHooks() {
	for _, h := range w.deps.Registry.List() {
		if h.Worktree == nil {
			continue
		}
		hooksSession := filepath.Join(h.Worktree.RepoRoot, ".swarm", "hooks", h.Session.ID)
		for _, event := range []string{"stop", "notify"} {
			path := filepath.Join(hooksSession, event)
			if _, err := os.Stat(path); err == nil {
				_ = os.Remove(path)
				if h.Session.Status == session.StatusRunning {
					w.deps.Registry.SetStatus(h.Session.ID, session.StatusAwaitingInput)
				}
			}
		}
		if h.Session.ClaudeSessionID == "" {
			path := filepath.Join(hooksSession, "session_start")
			if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
				if id := extractSessionID(data); id != "" {
					h.Session.ClaudeSessionID = id
					// Triggers persist via the SetStatus write path.
					w.deps.Registry.SetStatus(h.Session.ID, h.Session.Status)
				}
			}
		}
	}
}

// extractSessionID pulls "session_id" out of the JSON payload Claude sends
// to SessionStart hooks. Tolerant of the field appearing anywhere; we don't
// fully decode the JSON to keep the dependency footprint zero.
func extractSessionID(payload []byte) string {
	const key = `"session_id"`
	idx := strings.Index(string(payload), key)
	if idx < 0 {
		return ""
	}
	rest := string(payload[idx+len(key):])
	// Skip past the colon and any whitespace, expect a quote.
	rest = strings.TrimLeft(rest, ": \t\r\n")
	if !strings.HasPrefix(rest, `"`) {
		return ""
	}
	rest = rest[1:]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// evaluateAwaitingInput flips every Running session that's been silent
// past awaitingInputThreshold to AwaitingInput. Sessions in any other
// status (complete, killed, failed, interrupted, awaiting-input) are
// left alone — their state isn't being maintained by the heuristic.
func (w *Workspace) evaluateAwaitingInput() {
	now := time.Now()
	for _, h := range w.deps.Registry.List() {
		if h.Session.Status != session.StatusRunning {
			continue
		}
		last, ok := w.lastActivity[h.Session.ID]
		if !ok {
			continue
		}
		if now.Sub(last) >= awaitingInputThreshold {
			w.deps.Registry.SetStatus(h.Session.ID, session.StatusAwaitingInput)
		}
	}
}

// resizeAllSessions resizes both the virtual terminal and the agent's PTY
// for every active session whenever the window size changes.
func (w *Workspace) resizeAllSessions() {
	cols, rows := w.mainPaneSize()
	for id, term := range w.terminals {
		term.Resize(cols, rows)
		if h, ok := w.deps.Registry.Get(id); ok && h.Agent != nil {
			_ = h.Agent.Resize(cols, rows)
		}
	}
	for id, term := range w.shellTerminals {
		term.Resize(cols, rows)
		if sh, ok := w.shells[id]; ok {
			_ = sh.Resize(cols, rows)
		}
	}
}

func (w *Workspace) setToast(s string) {
	w.toast = s
	w.toastUntil = time.Now().Add(4 * time.Second)
}

// ----- spawn pipeline -----

// startSession runs the heavy work (worktree create-or-attach + agent
// spawn) off the UI goroutine. The result is delivered as a Bubbletea
// message so all state mutations stay single-threaded inside Update.
//
// Worktree dir name comes from the user-supplied label when set
// (slugified for filesystem safety), else the auto-generated session ID.
// If a worktree already exists at the resolved path we attach the new
// session to it instead of creating fresh, unless another live session
// already owns it.
// isolatedWorktreeGuidance is appended to the spawned agent's system prompt
// so it knows it's already running in a swarm-managed worktree and shouldn't
// stack another one on top.
const isolatedWorktreeGuidance = "You are already running inside an isolated git worktree managed by swarm. " +
	"Work directly in the current directory. Do NOT create git worktrees or use worktree isolation for subagents."

func (w Workspace) startSession(repo, prompt, name, agentName string, enableMCP bool) tea.Cmd {
	// Refusal check happens here on the UI goroutine so we can read the
	// registry safely. Same pass also picks up the ClaudeSessionID of any
	// existing handle for this worktree so we can reattach to the same
	// conversation rather than starting fresh.
	dirName := worktreeDirName(name)        // flat, stable session ID
	branchName := branchNameFromLabel(name) // slash-preserving git branch
	relPath := worktreeRelPath(name)        // nested on-disk dir, mirrors branch
	var resumeID string
	var existingPath string // an already-known worktree path for this ID
	if dirName == "" {
		// No name given: generate a fresh, unambiguous one. Branch and dir
		// match (no slashes to nest).
		dirName = w.deps.Registry.NextID()
		branchName = dirName
		relPath = dirName
	} else {
		for _, h := range w.deps.Registry.List() {
			// Match on the stable ID, not the path: a legacy session's dir
			// may be flat (h-1234) while new ones nest (h/1234), but both
			// resolve to the same ID.
			if h.Session.ID != dirName || h.Worktree == nil {
				continue
			}
			if h.Agent != nil {
				err := fmt.Sprintf("worktree %q is already in use by session %s", dirName, h.Session.Label())
				return func() tea.Msg { return spawnErrorMsg{Err: err} }
			}
			// Restored / interrupted handle for the same worktree:
			// inherit its captured Claude session id and reuse its actual
			// on-disk path (which may predate nested layout).
			if h.Session.ClaudeSessionID != "" {
				resumeID = h.Session.ClaudeSessionID
			}
			existingPath = h.Worktree.Path
		}
	}

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		// Prefer a known handle's path (covers legacy flat worktrees);
		// otherwise nest under the worktrees dir to mirror the branch.
		// Decide create-vs-attach by whether that path exists on disk.
		path := existingPath
		if path == "" {
			path = filepath.Join(worktree.SwarmWorktreesDir(repo), filepath.FromSlash(relPath))
		}
		var wt *worktree.Worktree
		var err error
		if _, statErr := os.Stat(path); statErr == nil {
			// Reuse the existing worktree. Read its actual checked-out branch
			// (authoritative — the dir slug can't recover a slashed branch
			// name like "h/1234") and its current HEAD as the diff base.
			branch := worktree.CurrentBranch(ctx, path)
			if branch == "" {
				branch = branchName
			}
			// Migrate legacy swarm/<slug> branches to the clean session
			// branch so commits fast-forward the remote feature branch.
			if reconcileLegacyBranch(ctx, path, branch, branchName) {
				branch = branchName
			}
			wt = &worktree.Worktree{
				ID:       dirName,
				Path:     path,
				BaseRef:  "HEAD",
				Branch:   branch,
				RepoRoot: repo,
			}
		} else {
			wt, err = w.deps.Git.Create(ctx, repo, "HEAD", dirName, relPath, branchName)
			if err != nil {
				return spawnErrorMsg{Err: "worktree: " + err.Error()}
			}
			// Pre-install/configure the fresh worktree (e.g. npm install)
			// before the agent starts. No-op unless the repo ships a
			// .swarm/setup.{sh,ps1}. Clean up if it fails so we don't strand
			// a half-prepared worktree.
			if setupErr := worktree.RunSetupHook(repo, wt.Path); setupErr != nil {
				_ = w.deps.Git.Destroy(context.Background(), wt)
				return spawnErrorMsg{Err: setupErr.Error()}
			}
		}

		factory := w.deps.agentFactory(agentName)
		if factory == nil {
			return spawnErrorMsg{Err: "no agent factory configured"}
		}
		a := factory()
		hooksDir := filepath.Join(repo, ".swarm", "hooks")
		_ = os.MkdirAll(filepath.Join(hooksDir, dirName), 0755)
		// Inject project memory only on a fresh conversation. When we're
		// resuming, the agent already has the prior context; piling
		// memory on top would duplicate it.
		effectivePrompt := prompt
		if resumeID == "" {
			effectivePrompt = memory.PromptWithMemory(repo, prompt)
		}
		opts := agent.SpawnOpts{
			Cwd:                wt.Path,
			Prompt:             effectivePrompt,
			SessionID:          dirName,
			HooksDir:           hooksDir,
			ResumeID:           resumeID,
			StrictMCP:          !enableMCP,
			AppendSystemPrompt: isolatedWorktreeGuidance,
		}
		if os.Getenv("SWARM_DUMP_PTY") != "" {
			dumpDir := filepath.Join(config.Home(), "dumps")
			if err := os.MkdirAll(dumpDir, 0755); err == nil {
				opts.DumpPath = filepath.Join(dumpDir, dirName+".log")
			}
		}
		if err := a.Spawn(context.Background(), opts); err != nil {
			// Don't auto-destroy a reused worktree on spawn failure — it
			// existed before we touched it. Only newly created worktrees
			// (where dirName == NextID-style) get cleaned.
			if strings.HasPrefix(dirName, "sess-") {
				_ = w.deps.Git.Destroy(context.Background(), wt)
			}
			return spawnErrorMsg{Err: "spawn: " + err.Error()}
		}
		now := time.Now()
		h := &session.Handle{
			Session: &session.Session{
				ID: dirName, Name: name, RepoRoot: repo, BaseRef: "HEAD",
				Branch:   wt.Branch,
				Worktree: wt.Path, AgentName: agentName,
				Prompt: prompt, Status: session.StatusRunning,
				CreatedAt: now, UpdatedAt: now,
				// Carry the captured resume id forward so it's
				// visible in state.json and persists across restarts.
				ClaudeSessionID: resumeID,
			},
			Worktree: wt, Agent: a,
		}
		w.deps.Registry.Add(h)
		return sessionSpawnedMsg{ID: dirName}
	}
}

// resumeSession relaunches the agent for a restored/interrupted handle in its
// existing worktree, reusing the same session ID (so the registry replaces the
// dead handle in place and the sidebar order is preserved). Resumes the Claude
// conversation when a session id was captured; otherwise just opens the agent
// in the worktree. Reported back via sessionSpawnedMsg.
func (w Workspace) resumeSession(h *session.Handle) tea.Cmd {
	s := h.Session
	wt := h.Worktree
	resumeID := s.ClaudeSessionID
	agentName := s.AgentName
	repo := s.RepoRoot
	createdAt := s.CreatedAt
	name, prompt, baseRef, branch := s.Name, s.Prompt, s.BaseRef, s.Branch
	id := s.ID
	factory := w.deps.agentFactory(agentName)
	return func() tea.Msg {
		if wt == nil {
			return spawnErrorMsg{Err: "resume: session has no worktree — discard it"}
		}
		if _, err := os.Stat(wt.Path); err != nil {
			return spawnErrorMsg{Err: "resume: worktree is gone — discard it"}
		}
		if factory == nil {
			return spawnErrorMsg{Err: "resume: no agent factory configured"}
		}
		a := factory()
		hooksDir := filepath.Join(repo, ".swarm", "hooks")
		_ = os.MkdirAll(filepath.Join(hooksDir, id), 0755)
		opts := agent.SpawnOpts{
			Cwd:                wt.Path,
			SessionID:          id,
			HooksDir:           hooksDir,
			ResumeID:           resumeID,
			StrictMCP:          true,
			AppendSystemPrompt: isolatedWorktreeGuidance,
		}
		if err := a.Spawn(context.Background(), opts); err != nil {
			return spawnErrorMsg{Err: "resume: " + err.Error()}
		}
		now := time.Now()
		w.deps.Registry.Add(&session.Handle{
			Session: &session.Session{
				ID: id, Name: name, RepoRoot: repo, BaseRef: baseRef, Branch: branch,
				Worktree: wt.Path, AgentName: agentName, Prompt: prompt,
				Status: session.StatusRunning, CreatedAt: createdAt, UpdatedAt: now,
				ClaudeSessionID: resumeID,
			},
			Worktree: wt, Agent: a,
		})
		return sessionSpawnedMsg{ID: id}
	}
}

// sessionSlugSegments lowercases a label and splits it into sanitized path
// segments — one per slash-delimited component, each reduced to runs of
// [a-z0-9] joined by single dashes. Empty segments are dropped. It's the
// shared basis for both the flat session ID (segments joined by "-") and the
// nested worktree directory (segments as subdirs), so the two never drift.
func sessionSlugSegments(label string) []string {
	label = strings.TrimSpace(strings.ToLower(label))
	var segs []string
	for _, part := range strings.Split(label, "/") {
		var b strings.Builder
		prevDash := false
		for _, r := range part {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
				b.WriteRune(r)
				prevDash = false
			case r == '-' || r == '_' || r == ' ':
				if !prevDash && b.Len() > 0 {
					b.WriteByte('-')
					prevDash = true
				}
			}
		}
		if s := strings.TrimRight(b.String(), "-"); s != "" {
			segs = append(segs, s)
		}
	}
	return segs
}

// worktreeDirName is the flat, filesystem-safe session ID: slug segments
// joined by dashes, so "h/1234" yields "h-1234". Stable across the move to
// nested worktree dirs, so existing sessions keep their IDs and reattach
// instead of duplicating. Returns "" when nothing survives (caller generates
// an auto ID). branchNameFromLabel derives the slash-preserving git branch.
func worktreeDirName(label string) string {
	return strings.Join(sessionSlugSegments(label), "-")
}

// worktreeRelPath is the worktree's on-disk directory relative to the swarm
// worktrees root, nested to mirror the branch: "h/1234-foo" stays
// "h/1234-foo" instead of flattening to a dash. Forward-slash separated;
// callers FromSlash it for the local OS. Returns "" when nothing survives.
func worktreeRelPath(label string) string {
	return strings.Join(sessionSlugSegments(label), "/")
}

// branchNameFromLabel turns a user-supplied label into a git branch name,
// preserving slashes so team conventions like "h/1234" or "feat/login" map
// to exactly that branch. Whitespace becomes dashes; characters illegal in a
// git ref are dropped; redundant or edge slashes/dashes are trimmed. Returns
// empty for an empty/all-illegal label (caller falls back to the dir ID).
func branchNameFromLabel(label string) string {
	label = strings.TrimSpace(label)
	var b strings.Builder
	prevSep := false // last written rune was a slash or dash
	for _, r := range label {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
			prevSep = r == '-'
		case r == '/':
			// Collapse repeats and never lead with a slash.
			if !prevSep && b.Len() > 0 {
				b.WriteByte('/')
				prevSep = true
			}
		case r == ' ' || r == '\t':
			if !prevSep && b.Len() > 0 {
				b.WriteByte('-')
				prevSep = true
			}
		}
		// Everything else (git-ref-illegal: ~^:?*[\@ etc.) is dropped.
	}
	out := strings.Trim(b.String(), "-/.")
	// Guard against ".." which git refs forbid.
	out = strings.ReplaceAll(out, "..", ".")
	return out
}

// reconcileLegacyBranch renames a worktree's legacy swarm/<slug> branch to the
// clean, session-derived branch (e.g. "h/1234") so the user's commits
// fast-forward the matching remote feature branch instead of a swarm-prefixed
// dead end. No-op (returns false) unless the checked-out branch is
// swarm-prefixed, a distinct target name is known, and that target doesn't
// already exist. Best-effort: a git failure just leaves the branch as-is.
func reconcileLegacyBranch(ctx context.Context, path, current, want string) bool {
	if want == "" || want == current || !strings.HasPrefix(current, "swarm/") {
		return false
	}
	// Don't clobber an existing branch of the target name.
	if exec.CommandContext(ctx, "git", "-C", path,
		"rev-parse", "--verify", "--quiet", "refs/heads/"+want).Run() == nil {
		return false
	}
	return exec.CommandContext(ctx, "git", "-C", path,
		"branch", "-m", current, want).Run() == nil
}

// refreshDiff runs `git -C <worktree> diff <baseRef>...HEAD --color=always`
// off the UI goroutine and reports the output back via diffRefreshedMsg.
// Color codes flow through to the rendered pane (we forced TrueColor at
// startup) so additions/deletions show in green/red.
func (w Workspace) refreshDiff(h *session.Handle) tea.Cmd {
	id := h.Session.ID
	wt := h.Worktree
	baseRef := h.Session.BaseRef
	if baseRef == "" {
		baseRef = "HEAD"
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// Diff the worktree's working tree against the merge-base of
		// HEAD and the original baseRef. For our spawn flow baseRef is
		// "HEAD" of the main repo at create time, captured as a SHA in
		// the worktree's reflog — comparing against it covers both
		// committed and uncommitted changes the agent made.
		args := []string{"-C", wt.Path, "diff", "--color=always", baseRef}
		out, err := exec.CommandContext(ctx, "git", args...).CombinedOutput()
		if err != nil {
			return diffRefreshedMsg{ID: id, Err: "diff: " + strings.TrimSpace(string(out))}
		}
		return diffRefreshedMsg{ID: id, Snapshot: parseDiff(string(out))}
	}
}

// refreshStats recomputes the +added/-deleted line counts for every live
// session off the UI goroutine via `git diff --numstat`. Cheap per session,
// but we run it on the slow stats tick rather than per render.
func (w Workspace) refreshStats() tea.Cmd {
	type item struct{ id, path, base string }
	var items []item
	for _, h := range w.deps.Registry.List() {
		if h.Worktree == nil {
			continue
		}
		// Only recompute sessions whose diff can still change (running /
		// awaiting), plus any session we don't have a stat for yet. A
		// finished session's churn is static — compute it once, then skip
		// it so a long-lived sidebar doesn't shell out to git every tick.
		_, have := w.diffStats[h.Session.ID]
		live := h.Session.Status == session.StatusRunning ||
			h.Session.Status == session.StatusAwaitingInput
		if have && !live {
			continue
		}
		base := h.Session.BaseRef
		if base == "" {
			base = "HEAD"
		}
		items = append(items, item{h.Session.ID, h.Worktree.Path, base})
	}
	if len(items) == 0 {
		return nil
	}
	return func() tea.Msg {
		stats := make(map[string]diffStat, len(items))
		for _, it := range items {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			out, err := exec.CommandContext(ctx, "git", "-C", it.path,
				"diff", "--numstat", it.base).Output()
			cancel()
			if err != nil {
				continue
			}
			stats[it.id] = parseNumstat(string(out))
		}
		return statsRefreshedMsg{stats: stats}
	}
}

// parseNumstat sums the added/deleted columns of `git diff --numstat`. Binary
// files report "-\t-" and are skipped.
func parseNumstat(out string) diffStat {
	var s diffStat
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if a, err := strconv.Atoi(fields[0]); err == nil {
			s.add += a
		}
		if d, err := strconv.Atoi(fields[1]); err == nil {
			s.del += d
		}
	}
	return s
}

// killSession terminates the focused session's agent. The worktree stays
// on disk so the user can review or later discard.
func (w Workspace) killSession(h *session.Handle) tea.Cmd {
	id := h.Session.ID
	return func() tea.Msg {
		_ = h.Agent.Kill()
		w.deps.Registry.SetStatus(id, session.StatusKilled)
		return sessionStatusMsg{ID: id}
	}
}

// discardSession kills the agent and shell (if running), destroys the
// worktree and its branch, and removes the session from the registry.
// Irreversible. The shell is captured on the UI goroutine to avoid racing
// the shells map.
func (w Workspace) discardSession(h *session.Handle) tea.Cmd {
	sh := w.shells[h.Session.ID]
	return func() tea.Msg {
		if h.Agent != nil {
			_ = h.Agent.Kill()
		}
		if sh != nil {
			_ = sh.Kill()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var warn string
		if h.Worktree != nil {
			if err := w.deps.Git.Destroy(ctx, h.Worktree); err != nil {
				// Don't strand the session in the list — the user asked for
				// it gone. Remove it anyway; a leftover worktree dir is a
				// `swarm prune` concern, not a reason to get stuck.
				warn = "discarded; worktree cleanup failed (run `swarm prune`): " + err.Error()
			}
		}
		w.deps.Registry.Remove(h.Session.ID)
		return sessionRemovedMsg{ID: h.Session.ID, Warn: warn}
	}
}

// shutdownAndQuit kills every active agent and shell in parallel, then emits
// QuitMsg so Bubbletea exits. Worktrees stay on disk per spec — discard or
// `swarm prune` are the only paths that destroy them.
func (w Workspace) shutdownAndQuit() tea.Cmd {
	return func() tea.Msg {
		var wg sync.WaitGroup
		kill := func(a agent.Agent) {
			defer wg.Done()
			_ = a.Kill()
		}
		for _, h := range w.deps.Registry.List() {
			if h.Agent != nil {
				wg.Add(1)
				go kill(h.Agent)
			}
		}
		for _, sh := range w.shells {
			wg.Add(1)
			go kill(sh)
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
//
// The palette and every shared style live in theme.go — a single cool, teal-
// accented token set so colors never drift between components.

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

	var rendered string
	switch w.mode {
	case ModeNewSession:
		rendered = lipgloss.Place(w.width, w.height, lipgloss.Center, lipgloss.Center, w.modal.View(),
			lipgloss.WithWhitespaceBackground(colorBg))
	case ModePicker:
		rendered = lipgloss.Place(w.width, w.height, lipgloss.Center, lipgloss.Center, w.picker.View(),
			lipgloss.WithWhitespaceBackground(colorBg))
	case ModeConfirm:
		rendered = lipgloss.Place(w.width, w.height, lipgloss.Center, lipgloss.Center, w.renderConfirm(),
			lipgloss.WithWhitespaceBackground(colorBg))
	case ModeMemory:
		rendered = lipgloss.Place(w.width, w.height, lipgloss.Center, lipgloss.Center, w.memModal.View(),
			lipgloss.WithWhitespaceBackground(colorBg))
	default:
		rendered = view
	}
	// Wrap the whole frame in the cool slate background so empty cells around
	// panes get the canvas color instead of stark terminal black. Embedded
	// ANSI in cell content overrides locally, so colored output isn't affected.
	return base.Render(rendered)
}

func (w Workspace) renderConfirm() string {
	body := base.Foreground(colorTextHi).Render(w.confirmPrompt) + "\n\n" +
		modalHint.Render("y confirm · n / esc cancel")
	return confirmBorder.Render(body)
}

func (w Workspace) renderBody() string {
	sidebarW := 30
	mainW := w.width - sidebarW - 4
	bodyH := w.height - 3

	// The teal focus border follows input: the main pane owns it while
	// attached, the sidebar owns it while navigating sessions.
	sidebarBorder, mainBorder := paneBorderFocus, paneBorder
	if w.mode == ModeAttached {
		sidebarBorder, mainBorder = paneBorder, paneBorderFocus
	}
	sidebar := sidebarBorder.Width(sidebarW).Height(bodyH).Render(w.renderSidebar(sidebarW, bodyH))
	main := mainBorder.Width(mainW).Height(bodyH).Render(w.renderMain(mainW, bodyH-2))
	return lipgloss.JoinHorizontal(lipgloss.Top, sidebar, main)
}

func (w Workspace) renderSidebar(width, height int) string {
	list := w.displayList()
	if len(list) == 0 {
		return sidebarTitle.Render("SESSIONS") + "\n" +
			dim.Render(strings.Repeat("─", width-2)) + "\n" +
			dim.Render("(no sessions)\npress n to spawn")
	}
	// Page the list so it never overflows the pane: each session occupies
	// ~3 lines (label, repo+stats, spacer); header+rule take 2. Window
	// around the focused entry so it's always visible, with ▲/▼ counts for
	// what's scrolled off.
	const perSession = 3
	fit := max((height-2)/perSession, 1)
	start := 0
	if len(list) > fit {
		focusIdx := 0
		for i, h := range list {
			if h.Session.ID == w.focused {
				focusIdx = i
				break
			}
		}
		start = min(max(focusIdx-fit/2, 0), len(list)-fit)
	}
	end := min(start+fit, len(list))
	window := list[start:end]

	header := sidebarTitle.Render("SESSIONS") + base.Foreground(colorTextFaint).Render(fmt.Sprintf("  %d", len(list)))
	if start > 0 {
		header += dim.Render(fmt.Sprintf("  ▲%d", start))
	}
	rows := []string{header, dim.Render(strings.Repeat("─", width-2))}
	for i, h := range window {
		// Status glyph + label gets prominence so the user can scan a
		// long sidebar and spot what needs them.
		var statusBit string
		switch h.Session.Status {
		case session.StatusAwaitingInput:
			statusBit = awaitTag.Render("◆ awaiting")
		case session.StatusRunning:
			statusBit = runTag.Render("● running")
		case session.StatusInterrupted:
			statusBit = dim.Render("⊘ interrupted")
		default:
			statusBit = dim.Render("· " + h.Session.Status.String())
		}
		// Two-line entry per session: label + status on top, repo on a
		// dimmed second line. Plus a blank line between entries so the
		// sidebar reads cleanly when sessions multiply.
		topLine := fmt.Sprintf("%s  %s", h.Session.Label(), statusBit)
		// Second line: repo name plus +added/-deleted diff stats, the way
		// Claude Squad surfaces churn at a glance.
		// Separators go through base (slate bg) too — a plain " " here would
		// sit after a styled segment's reset and render as terminal black.
		var bottomLine string
		if name := filepath.Base(h.Session.RepoRoot); name != "" {
			bottomLine = repoTag.Render(name)
		}
		if st, ok := w.diffStats[h.Session.ID]; ok && (st.add > 0 || st.del > 0) {
			stats := addStat.Render(fmt.Sprintf("+%d", st.add)) + base.Render(" ") +
				delStat.Render(fmt.Sprintf("-%d", st.del))
			if bottomLine != "" {
				bottomLine += base.Render("  ") + stats
			} else {
				bottomLine = stats
			}
		}
		if h.Session.ID == w.focused {
			rows = append(rows, rowFocus.Render("▎ "+topLine))
			if bottomLine != "" {
				rows = append(rows, "  "+bottomLine)
			}
		} else {
			rows = append(rows, rowDim.Render("  "+topLine))
			if bottomLine != "" {
				rows = append(rows, "  "+bottomLine)
			}
		}
		if i < len(window)-1 {
			rows = append(rows, "")
		}
	}
	if end < len(list) {
		rows = append(rows, dim.Render(fmt.Sprintf("  ▼%d more", len(list)-end)))
	}
	return strings.Join(rows, "\n")
}

func (w Workspace) renderMain(width, height int) string {
	if w.focused == "" {
		title := base.Foreground(colorAccentSoft).Bold(true).Render("swarm") +
			base.Foreground(colorTextFaint).Render("  v0.0.1-dev")
		return title + "\n\n" +
			base.Foreground(colorTextMid).Render("Run multiple agents in parallel — each in its own worktree.") + "\n\n" +
			dim.Render("press ") + keybarKey.Render("n") + dim.Render(" to spawn a session.") + "\n\n" +
			dim.Render("q quit · n new · j/k navigate · enter attach\n"+
				"tab cycle preview/diff/shell · x kill · d discard · m memory")
	}
	// Tab bar mirrors Claude Squad: Preview (live output) | Diff | Shell.
	// The active chip is filled; tab cycles through them.
	tabs := w.renderTabs()
	contentH := max(height-1, 1)
	var content string
	switch w.viewMode {
	case ViewDiff:
		content = w.renderDiff(width, contentH)
	case ViewShell:
		content = cropTerminal(w.shellTerminals[w.focused], contentH,
			dim.Render("starting shell… (enter to attach)"))
	default:
		content = cropTerminal(w.terminals[w.focused], contentH,
			dim.Render("waiting for output…"))
	}
	return tabs + "\n" + content
}

// cropTerminal renders a session terminal cropped to its last `height` lines,
// or `placeholder` when the terminal isn't ready yet.
func cropTerminal(term *SessionTerminal, height int, placeholder string) string {
	if term == nil {
		return placeholder
	}
	lines := strings.Split(strings.TrimRight(term.Render(), "\n"), "\n")
	if len(lines) > height {
		lines = lines[len(lines)-height:]
	}
	return strings.Join(lines, "\n")
}

// renderTabs draws the Preview | Diff | Shell tab strip for the main pane.
func (w Workspace) renderTabs() string {
	tab := func(label string, mode ViewMode) string {
		if w.viewMode == mode {
			return tabActive.Render(label)
		}
		return tabInactive.Render(label)
	}
	return tab("Preview", ViewLive) + tab("Diff", ViewDiff) + tab("Shell", ViewShell)
}

// renderDiff is read-only review: a file list above the cursor file's diff
// content. j/k navigate files, r refreshes. Integration happens in the
// Shell tab, so there's no keep/discard or accept here.
func (w Workspace) renderDiff(width, height int) string {
	// Every line must be cropped to the pane's content width. Raw `git diff`
	// lines are arbitrarily long, and if any exceeds the width, lipgloss
	// word-wraps the ANSI-colored block when it renders the bordered pane —
	// miscounting widths and scattering the border and line tails across the
	// screen. Truncating up front keeps each line within bounds. ansi.Truncate
	// is escape-aware so it crops by visible cells and closes open styles.
	crop := func(s string) string {
		if width <= 0 {
			return s
		}
		return ansi.Truncate(s, width, "")
	}

	header := dim.Render("j/k file · ctrl+d/u scroll · h/l ←→ · r refresh · shell tab to commit")
	snap := w.diffSnapshots[w.focused]
	if snap == nil {
		return header + "\n" + dim.Render("loading diff…")
	}
	if len(snap.Files) == 0 {
		return header + "\n" + dim.Render("(no changes)")
	}

	// Top: file list. Reserve 1 line per file, capped so we always leave
	// at least half the pane for diff content.
	maxList := min(len(snap.Files), max(height/2, 1))
	listLines := make([]string, 0, maxList)
	for i, f := range snap.Files {
		if i >= maxList {
			listLines = append(listLines, dim.Render(fmt.Sprintf("  …and %d more", len(snap.Files)-maxList)))
			break
		}
		row := "  " + f.Path
		if i == snap.Cursor {
			row = rowFocus.Render("▎ " + f.Path)
		}
		listLines = append(listLines, crop(row))
	}

	separator := dim.Render(strings.Repeat("─", 40))

	// Bottom: diff content of the selected file, windowed by ScrollY and
	// cropped to remaining rows.
	contentBudget := max(height-len(listLines)-3, 1)
	contentLines := strings.Split(strings.TrimRight(snap.Files[snap.Cursor].Content, "\n"), "\n")
	// Clamp scroll so the last page is the furthest you can go.
	maxScroll := max(len(contentLines)-contentBudget, 0)
	if snap.ScrollY > maxScroll {
		snap.ScrollY = maxScroll
	}
	contentLines = contentLines[snap.ScrollY:]
	if len(contentLines) > contentBudget {
		contentLines = contentLines[:contentBudget]
	}
	// Clamp horizontal scroll to the widest visible line so you can't scroll
	// past the content into empty space.
	if width > 0 {
		widest := 0
		for _, line := range contentLines {
			if lw := ansi.StringWidth(line); lw > widest {
				widest = lw
			}
		}
		snap.ScrollX = max(min(snap.ScrollX, max(widest-width, 0)), 0)
		// ansi.Cut returns the [left, right) cell window, escape-aware, so a
		// horizontal offset never breaks a color span.
		for i, line := range contentLines {
			contentLines[i] = ansi.Cut(line, snap.ScrollX, snap.ScrollX+width)
		}
	}
	if maxScroll > 0 {
		separator = dim.Render(fmt.Sprintf("─── lines %d-%d/%d ",
			snap.ScrollY+1, snap.ScrollY+len(contentLines), len(strings.Split(strings.TrimRight(snap.Files[snap.Cursor].Content, "\n"), "\n")))) +
			dim.Render(strings.Repeat("─", 12))
	}

	return strings.Join(append(append([]string{header}, listLines...), append([]string{separator}, contentLines...)...), "\n")
}

func (w Workspace) renderStatus() string {
	bar := w.renderKeybar()
	if w.toast != "" && time.Now().Before(w.toastUntil) {
		return bar + "  " + toastBox.Render("⚠ "+w.toast)
	}
	return bar
}

// renderKeybar is the Claude Squad-style shortcut strip along the bottom.
// Contents depend on the current mode and view so the user always sees the
// keys that apply right now.
func (w Workspace) renderKeybar() string {
	type bind struct{ key, label string }
	var binds []bind
	switch {
	case w.mode == ModeAttached:
		dest := "agent"
		if w.viewMode == ViewShell {
			dest = "shell"
		}
		return attachTag.Render("ATTACHED") + "  " +
			keybar.Render("keys → "+dest+" · ") + keybarKey.Render("ctrl+q") + keybar.Render(" detach")
	case w.focused != "" && w.viewMode == ViewDiff:
		binds = []bind{
			{"tab", "shell"}, {"j/k", "file"}, {"h/l", "scroll"},
			{"r", "refresh"}, {"x", "kill"}, {"d", "discard"}, {"q", "quit"},
		}
	case w.focused != "" && w.viewMode == ViewShell:
		binds = []bind{
			{"↵", "attach"}, {"tab", "preview"}, {"j/k", "nav"},
			{"d", "discard"}, {"q", "quit"},
		}
	default:
		binds = []bind{
			{"n", "new"}, {"↵", "attach"}, {"tab", "diff"}, {"j/k", "nav"},
			{"x", "kill"}, {"d", "discard"}, {"m", "memory"}, {"q", "quit"},
		}
	}
	parts := make([]string, len(binds))
	for i, b := range binds {
		parts[i] = keybarKey.Render(b.key) + keybar.Render(" "+b.label)
	}
	return strings.Join(parts, keybar.Render(" · "))
}
