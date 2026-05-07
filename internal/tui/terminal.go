package tui

import (
	"bytes"
	"io"
	"strings"

	"github.com/micro-editor/terminal"
)

// SessionTerminal is the per-session virtual terminal. Bytes from the agent's
// PTY get fed in via Feed; the screen state is read out via Render and shown
// in the focused pane. Built on github.com/micro-editor/terminal — chosen
// because its alt-screen swap is properly implemented (state.go:swapScreen),
// which is what we need for Claude Code's TUI to survive detach/re-attach
// cycles.
//
// SessionTerminal is not safe for concurrent Feed/Resize, but the underlying
// State protects itself with a lock so Render can safely race with Feed if
// needed. Bubbletea's single-goroutine Update loop makes this academic.
type SessionTerminal struct {
	state      *terminal.State
	vt         *terminal.VT
	cols, rows int
}

// NewSessionTerminal constructs a virtual terminal sized to (cols, rows),
// clamped to a sane minimum so a 0-sized pane during early renders doesn't
// confuse the emulator.
func NewSessionTerminal(cols, rows int) *SessionTerminal {
	cols = clampMin(cols, 20)
	rows = clampMin(rows, 5)
	state := &terminal.State{}
	// Create requires a ReadCloser even when we never call Parse. We feed
	// bytes via Write() instead, so a dead source is fine.
	vt, _ := terminal.Create(state, io.NopCloser(bytes.NewReader(nil)))
	vt.Resize(cols, rows)
	return &SessionTerminal{state: state, vt: vt, cols: cols, rows: rows}
}

// Feed parses bytes from the agent into the virtual terminal. Despite the
// underlying API name, VT.Write doesn't write to a PTY — it parses input
// into screen state.
func (t *SessionTerminal) Feed(b []byte) {
	if len(b) == 0 {
		return
	}
	_, _ = t.vt.Write(b)
}

// Resize updates the virtual terminal dimensions. The agent's PTY should be
// resized to match — see Workspace.resizeAllSessions.
func (t *SessionTerminal) Resize(cols, rows int) {
	cols = clampMin(cols, 20)
	rows = clampMin(rows, 5)
	t.cols, t.rows = cols, rows
	t.vt.Resize(cols, rows)
}

// Render walks the cell grid and returns the screen as plain text. Color
// and attribute preservation is a separate polish layer (cell.fg, cell.bg
// are available — we just don't emit ANSI for them yet).
func (t *SessionTerminal) Render() string {
	t.state.Lock()
	defer t.state.Unlock()
	var sb strings.Builder
	for y := 0; y < t.rows; y++ {
		for x := 0; x < t.cols; x++ {
			ch, _, _ := t.state.Cell(x, y)
			if ch == 0 {
				ch = ' '
			}
			sb.WriteRune(ch)
		}
		if y < t.rows-1 {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// Size reports the current terminal dimensions.
func (t *SessionTerminal) Size() (cols, rows int) {
	return t.cols, t.rows
}

func clampMin(n, lo int) int {
	if n < lo {
		return lo
	}
	return n
}
