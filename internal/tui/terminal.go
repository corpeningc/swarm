package tui

import "github.com/hinshun/vt10x"

// SessionTerminal is the per-session virtual terminal. We feed it the bytes
// emitted by the agent's PTY and render its current screen state into the
// main pane. This is what makes Claude Code's cursor-positioned TUI render
// correctly inside Bubbletea instead of streaming raw escape codes.
//
// SessionTerminal is not safe for concurrent use; callers must serialize
// Feed / Resize / Render. Bubbletea's single-goroutine Update loop satisfies
// this naturally.
type SessionTerminal struct {
	term vt10x.Terminal
}

// NewSessionTerminal constructs a virtual terminal sized to (cols, rows),
// clamped to a sane minimum so a 0-sized pane during early renders doesn't
// crash vt10x.
func NewSessionTerminal(cols, rows int) *SessionTerminal {
	cols = clampMin(cols, 20)
	rows = clampMin(rows, 5)
	return &SessionTerminal{term: vt10x.New(vt10x.WithSize(cols, rows))}
}

// Feed writes bytes from the agent into the virtual terminal, parsing escape
// sequences and updating the screen.
func (t *SessionTerminal) Feed(b []byte) {
	if len(b) == 0 {
		return
	}
	_, _ = t.term.Write(b)
}

// Resize updates the virtual terminal dimensions. The agent's PTY should be
// resized to match — see Workspace.resizeAllSessions.
func (t *SessionTerminal) Resize(cols, rows int) {
	t.term.Resize(clampMin(cols, 20), clampMin(rows, 5))
}

// Render returns the current screen as plain text. Colors and attributes are
// not yet preserved; that's a polish layer (cell walk + ANSI re-emit) for
// later.
func (t *SessionTerminal) Render() string {
	return t.term.String()
}

// Size reports the current terminal dimensions.
func (t *SessionTerminal) Size() (cols, rows int) {
	return t.term.Size()
}

func clampMin(n, lo int) int {
	if n < lo {
		return lo
	}
	return n
}
