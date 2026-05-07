// Package tui implements the Bubbletea workspace UI.
package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	// ModeConfirm shows a yes/no prompt for destructive operations
	// (discard, accept). y confirms, n/esc cancels.
	ModeConfirm
)

// ViewMode picks what the main pane renders for the focused session —
// the live virtual terminal output, or a snapshot of the worktree's
// git diff against its base ref.
type ViewMode int

const (
	ViewLive ViewMode = iota
	ViewDiff
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

	// viewMode picks what the main pane shows for the focused session.
	viewMode ViewMode
	// diffSnapshots holds the most-recently-fetched parsed diff per
	// session, with the user's keep/discard selection. Refreshed on
	// tab-toggle or 'r'.
	diffSnapshots map[string]*DiffSnapshot

	// lastActivity is the wall-clock time of the most recent agent event
	// for each session, used to flip running ↔ awaiting-input.
	lastActivity map[string]time.Time

	toast      string
	toastUntil time.Time

	// confirmPrompt is rendered to the user while in ModeConfirm.
	// pendingAction runs when the user presses 'y'.
	confirmPrompt string
	pendingAction func() tea.Cmd

	quitting bool
}

func NewWorkspace(deps WorkspaceDeps) Workspace {
	return Workspace{
		deps:          deps,
		terminals:     make(map[string]*SessionTerminal),
		diffSnapshots: make(map[string]*DiffSnapshot),
		lastActivity:  make(map[string]time.Time),
	}
}

// activityTickInterval is how often we poll for "session idle long enough
// to flip to awaiting-input".
const activityTickInterval = 1 * time.Second

// awaitingInputThreshold is how long a running session must be silent
// before we treat it as waiting on the user.
const awaitingInputThreshold = 3 * time.Second

// activityTickMsg fires on the activityTickInterval to re-evaluate every
// running session's running ↔ awaiting-input flag.
type activityTickMsg struct{}

func tickActivity() tea.Cmd {
	return tea.Tick(activityTickInterval, func(time.Time) tea.Msg {
		return activityTickMsg{}
	})
}

func (w Workspace) Init() tea.Cmd { return tickActivity() }

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

// sessionRemovedMsg signals that a session is gone — discarded or
// accepted. Workspace cleans up its terminal map and focus.
type sessionRemovedMsg struct{ ID string }

// diffRefreshedMsg carries a freshly-parsed diff snapshot for a session.
type diffRefreshedMsg struct {
	ID       string
	Snapshot *DiffSnapshot
	Err      string
}

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
		w.lastActivity[m.ID] = time.Now()
		w.focused = m.ID
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
		w.checkHooks()
		w.evaluateAwaitingInput()
		return w, tickActivity()
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
	case sessionStatusMsg:
		// No-op other than triggering a redraw via the message round-trip.
		return w, nil
	case sessionRemovedMsg:
		delete(w.terminals, m.ID)
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
		if h, ok := w.focusedHandle(); ok {
			if h.Agent == nil {
				w.setToast("session is from a previous run; discard or accept")
			} else {
				w.mode = ModeAttached
			}
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
	case "A", "shift+a":
		// Accept ff-merges into the main repo's current branch. Bound to
		// shift+A to avoid accidental triggers from vim muscle memory
		// (vim's `a` is "append after cursor"). Lowercase `a` is a
		// no-op in idle mode for the same reason.
		if h, ok := w.focusedHandle(); ok {
			id := h.Session.Label()
			snap := w.diffSnapshots[w.focused]
			if w.viewMode == ViewDiff && snap != nil && len(snap.Files) > 0 {
				kept := len(snap.SelectedFiles())
				total := len(snap.Files)
				w.confirmPrompt = fmt.Sprintf("accept %s? %d of %d files kept", id, kept, total)
				w.pendingAction = func() tea.Cmd { return w.acceptSessionSelective(h, snap.DiscardedFiles()) }
			} else {
				w.confirmPrompt = "accept " + id + "? merge into current branch"
				w.pendingAction = func() tea.Cmd { return w.acceptSession(h) }
			}
			w.mode = ModeConfirm
		}
	case "tab":
		// Tab toggles the main pane between live agent output and the
		// session's git diff vs its base ref. (D / shift+D kept as
		// aliases for muscle memory but Tab is the primary binding.)
		if w.viewMode == ViewDiff {
			w.viewMode = ViewLive
		} else if h, ok := w.focusedHandle(); ok {
			w.viewMode = ViewDiff
			return w, w.refreshDiff(h)
		}
	case "D", "shift+d":
		if w.viewMode == ViewDiff {
			w.viewMode = ViewLive
		} else if h, ok := w.focusedHandle(); ok {
			w.viewMode = ViewDiff
			return w, w.refreshDiff(h)
		}
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

// handleDiffKey is the diff-view-specific keymap. j/k navigate files,
// space toggles keep/discard for the highlighted file. Returns
// (model, cmd, handled): handled=false means handleIdleKey should
// continue with its session-level keymap.
func (w Workspace) handleDiffKey(k tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	snap := w.diffSnapshots[w.focused]
	if snap == nil || len(snap.Files) == 0 {
		return w, nil, false
	}
	switch k.String() {
	case "j", "down":
		snap.Cursor = (snap.Cursor + 1) % len(snap.Files)
		return w, nil, true
	case "k", "up":
		snap.Cursor = (snap.Cursor - 1 + len(snap.Files)) % len(snap.Files)
		return w, nil, true
	case " ", "space":
		snap.Files[snap.Cursor].Keep = !snap.Files[snap.Cursor].Keep
		return w, nil, true
	}
	return w, nil, false
}

// focusedHandle returns the registered Handle for the currently focused
// session, or (nil, false) if there is no focus or the focus is stale.
func (w Workspace) focusedHandle() (*session.Handle, bool) {
	if w.focused == "" {
		return nil, false
	}
	return w.deps.Registry.Get(w.focused)
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

// startSession runs the heavy work (worktree create-or-attach + agent
// spawn) off the UI goroutine. The result is delivered as a Bubbletea
// message so all state mutations stay single-threaded inside Update.
//
// Worktree dir name comes from the user-supplied label when set
// (slugified for filesystem safety), else the auto-generated session ID.
// If a worktree already exists at the resolved path we attach the new
// session to it instead of creating fresh, unless another live session
// already owns it.
func (w Workspace) startSession(repo, prompt, name string) tea.Cmd {
	// Refusal check happens here on the UI goroutine so we can read the
	// registry safely. Same pass also picks up the ClaudeSessionID of any
	// existing handle for this worktree so we can reattach to the same
	// conversation rather than starting fresh.
	dirName := worktreeDirName(name)
	var resumeID string
	if dirName == "" {
		// No name given: generate a fresh, unambiguous one.
		dirName = w.deps.Registry.NextID()
	} else {
		for _, h := range w.deps.Registry.List() {
			if h.Worktree == nil || filepath.Base(h.Worktree.Path) != dirName {
				continue
			}
			if h.Agent != nil {
				err := fmt.Sprintf("worktree %q is already in use by session %s", dirName, h.Session.Label())
				return func() tea.Msg { return spawnErrorMsg{Err: err} }
			}
			// Restored / interrupted handle for the same worktree:
			// inherit its captured Claude session id.
			if h.Session.ClaudeSessionID != "" {
				resumeID = h.Session.ClaudeSessionID
			}
		}
	}

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		// Decide create-vs-attach by checking whether the dir exists.
		path := filepath.Join(worktree.SwarmWorktreesDir(repo), dirName)
		var wt *worktree.Worktree
		var err error
		if _, statErr := os.Stat(path); statErr == nil {
			// Reuse the existing worktree. Read its current HEAD as the
			// effective base ref.
			wt = &worktree.Worktree{
				ID:       dirName,
				Path:     path,
				BaseRef:  "HEAD",
				RepoRoot: repo,
			}
		} else {
			wt, err = w.deps.Git.Create(ctx, repo, "HEAD", dirName)
			if err != nil {
				return spawnErrorMsg{Err: "worktree: " + err.Error()}
			}
		}

		a := w.deps.AgentFactory()
		hooksDir := filepath.Join(repo, ".swarm", "hooks")
		_ = os.MkdirAll(filepath.Join(hooksDir, dirName), 0755)
		opts := agent.SpawnOpts{
			Cwd:       wt.Path,
			Prompt:    prompt,
			SessionID: dirName,
			HooksDir:  hooksDir,
			ResumeID:  resumeID,
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
				Worktree: wt.Path, AgentName: "claude-code",
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

// worktreeDirName slugifies a user-supplied label into a filesystem-safe
// directory name. Returns empty if the result would be empty (caller
// generates an auto ID instead).
func worktreeDirName(label string) string {
	label = strings.TrimSpace(strings.ToLower(label))
	if label == "" {
		return ""
	}
	var b strings.Builder
	prevDash := false
	for _, r := range label {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		case r == '-' || r == '_' || r == ' ' || r == '/':
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
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

// discardSession kills the agent (if running), destroys the worktree, and
// removes the session from the registry. Irreversible.
func (w Workspace) discardSession(h *session.Handle) tea.Cmd {
	return func() tea.Msg {
		if h.Agent != nil {
			_ = h.Agent.Kill()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := w.deps.Git.Destroy(ctx, h.Worktree); err != nil {
			return spawnErrorMsg{Err: "discard: " + err.Error()}
		}
		w.deps.Registry.Remove(h.Session.ID)
		return sessionRemovedMsg{ID: h.Session.ID}
	}
}

// acceptSessionSelective is the file-level cousin of acceptSession.
// discardFiles are reverted in the worktree before commit-and-merge.
func (w Workspace) acceptSessionSelective(h *session.Handle, discardFiles []string) tea.Cmd {
	return func() tea.Msg {
		if h.Agent != nil {
			_ = h.Agent.Kill()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := w.deps.Git.AcceptSelective(ctx, h.Worktree, discardFiles); err != nil {
			return spawnErrorMsg{Err: "accept: " + err.Error()}
		}
		w.deps.Registry.Remove(h.Session.ID)
		return sessionRemovedMsg{ID: h.Session.ID}
	}
}

// acceptSession ff-merges the worktree's HEAD into the main repo's current
// branch via worktree.Manager.Accept (which also destroys the worktree on
// success), then removes the session from the registry. The agent is killed
// first so its grip on PTY/files doesn't block the merge.
func (w Workspace) acceptSession(h *session.Handle) tea.Cmd {
	return func() tea.Msg {
		if h.Agent != nil {
			_ = h.Agent.Kill()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := w.deps.Git.Accept(ctx, h.Worktree); err != nil {
			return spawnErrorMsg{Err: "accept: " + err.Error()}
		}
		w.deps.Registry.Remove(h.Session.ID)
		return sessionRemovedMsg{ID: h.Session.ID}
	}
}

// shutdownAndQuit kills every active agent in parallel, then emits QuitMsg
// so Bubbletea exits. Worktrees stay on disk per spec — accept/discard or
// `swarm prune` are the only paths that destroy them.
func (w Workspace) shutdownAndQuit() tea.Cmd {
	return func() tea.Msg {
		var wg sync.WaitGroup
		for _, h := range w.deps.Registry.List() {
			if h.Agent == nil {
				continue
			}
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
	awaitTag   = lipgloss.NewStyle().Foreground(lipgloss.Color("220")).Bold(true)
	runTag     = lipgloss.NewStyle().Foreground(lipgloss.Color("117"))
	hintTag    = lipgloss.NewStyle().Foreground(lipgloss.Color("141")).Italic(true)

	// workspaceBg is the deep-purple tint we apply behind the whole TUI
	// so panes don't sit on stark terminal black. Picked to match the
	// ambient "we're in a special multi-agent context" vibe.
	workspaceBg = lipgloss.Color("#1a0f26")
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

	var rendered string
	switch w.mode {
	case ModeNewSession:
		rendered = lipgloss.Place(w.width, w.height, lipgloss.Center, lipgloss.Center, w.modal.View(),
			lipgloss.WithWhitespaceBackground(workspaceBg))
	case ModePicker:
		rendered = lipgloss.Place(w.width, w.height, lipgloss.Center, lipgloss.Center, w.picker.View(),
			lipgloss.WithWhitespaceBackground(workspaceBg))
	case ModeConfirm:
		rendered = lipgloss.Place(w.width, w.height, lipgloss.Center, lipgloss.Center, w.renderConfirm(),
			lipgloss.WithWhitespaceBackground(workspaceBg))
	default:
		rendered = view
	}
	// Wrap the whole frame in the workspace's purple-tinted background
	// so empty cells around panes get the brand color instead of stark
	// terminal black. Embedded ANSI in cell content overrides locally,
	// so colored output isn't affected.
	return lipgloss.NewStyle().Background(workspaceBg).Render(rendered)
}

func (w Workspace) renderConfirm() string {
	body := w.confirmPrompt + "\n\n" + modalHint.Render("y confirm · n / esc cancel")
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("196")).
		Padding(1, 2).
		Render(body)
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
	for i, h := range list {
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
		var bottomLine string
		if name := filepath.Base(h.Session.RepoRoot); name != "" {
			bottomLine = repoTag.Render("  " + name)
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
		if i < len(list)-1 {
			rows = append(rows, "")
		}
	}
	return strings.Join(rows, "\n")
}

func (w Workspace) renderMain(_, height int) string {
	if w.focused == "" {
		return "Swarm v0.0.1-dev\n\n" +
			dim.Render("welcome. press n to spawn a session.\n\n"+
				"q quit · n new · j/k navigate · enter attach\n"+
				"tab diff/live · x kill · A accept · d discard")
	}
	if w.viewMode == ViewDiff {
		return w.renderDiff(height)
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

// renderDiff lays out the per-file selection list above the diff content
// of the cursor's file. j/k or arrows navigate files, space toggles
// keep/discard, 'a' starts the (selection-aware) accept flow.
func (w Workspace) renderDiff(height int) string {
	header := dim.Render("DIFF · tab back · j/k file · space keep/discard · a accept · r refresh")
	snap := w.diffSnapshots[w.focused]
	if snap == nil {
		return header + "\n" + dim.Render("loading diff…")
	}
	if len(snap.Files) == 0 {
		return header + "\n" + dim.Render("(no changes)")
	}

	// Top: file list. Reserve 1 line per file, capped so we always leave
	// at least half the pane for diff content.
	maxList := len(snap.Files)
	if maxList > height/2 {
		maxList = height / 2
	}
	if maxList < 1 {
		maxList = 1
	}
	listLines := make([]string, 0, maxList)
	for i, f := range snap.Files {
		if i >= maxList {
			listLines = append(listLines, dim.Render(fmt.Sprintf("  …and %d more", len(snap.Files)-maxList)))
			break
		}
		mark := "[ ]"
		if f.Keep {
			mark = awaitTag.Render("[✓]")
		} else {
			mark = dim.Render("[ ]")
		}
		row := fmt.Sprintf("%s %s", mark, f.Path)
		if i == snap.Cursor {
			row = rowFocus.Render("▎ " + row)
		} else {
			row = "  " + row
		}
		listLines = append(listLines, row)
	}

	separator := dim.Render(strings.Repeat("─", 40))

	// Bottom: diff content of the selected file, cropped to remaining
	// rows.
	contentBudget := height - len(listLines) - 3
	if contentBudget < 1 {
		contentBudget = 1
	}
	contentLines := strings.Split(strings.TrimRight(snap.Files[snap.Cursor].Content, "\n"), "\n")
	if len(contentLines) > contentBudget {
		contentLines = contentLines[:contentBudget]
	}

	return strings.Join(append(append([]string{header}, listLines...), append([]string{separator}, contentLines...)...), "\n")
}

func (w Workspace) renderStatus() string {
	var head string
	if w.mode == ModeAttached {
		head = attachTag.Render("ATTACHED · ctrl+q to detach") + "  "
	} else if w.focused != "" && w.viewMode == ViewLive {
		// Always-visible affordance: tab gets you the diff. Discoverable
		// without reading help text.
		head = hintTag.Render("tab → diff") + "  "
	} else if w.focused != "" && w.viewMode == ViewDiff {
		head = hintTag.Render("tab → live") + "  "
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
