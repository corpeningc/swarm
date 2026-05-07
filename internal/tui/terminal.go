package tui

import (
	"bytes"
	"fmt"
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

// Render walks the cell grid and returns the screen as a string with ANSI
// color codes embedded. We track the last fg/bg we emitted and only write
// new SGR sequences when a cell's color changes — keeping the output
// reasonably compact even on full-color screens.
func (t *SessionTerminal) Render() string {
	t.state.Lock()
	defer t.state.Unlock()

	var sb strings.Builder
	// Sentinel values we'll never match on first cell, forcing both codes
	// to emit on the first painted glyph.
	var lastFg, lastBg terminal.Color = 0xffff, 0xffff

	for y := 0; y < t.rows; y++ {
		for x := 0; x < t.cols; x++ {
			ch, fg, bg := t.state.Cell(x, y)
			if ch == 0 {
				ch = ' '
			}
			if fg != lastFg {
				sb.WriteString(fgCode(fg))
				lastFg = fg
			}
			if bg != lastBg {
				sb.WriteString(bgCode(bg))
				lastBg = bg
			}
			sb.WriteRune(ch)
		}
		if y < t.rows-1 {
			sb.WriteByte('\n')
		}
	}
	sb.WriteString("\x1b[0m")
	return sb.String()
}

// fgCode emits the SGR sequence that sets the foreground to c. Covers the
// 16 ANSI colors (0-15), the xterm 256-color palette (16-255), and the
// DefaultFG sentinel.
func fgCode(c terminal.Color) string {
	switch {
	case c == terminal.DefaultFG:
		return "\x1b[39m"
	case c < 8:
		return fmt.Sprintf("\x1b[3%dm", c)
	case c < 16:
		return fmt.Sprintf("\x1b[9%dm", c-8)
	case c < 256:
		return fmt.Sprintf("\x1b[38;5;%dm", c)
	}
	return "\x1b[39m"
}

// bgCode is the background equivalent of fgCode.
func bgCode(c terminal.Color) string {
	switch {
	case c == terminal.DefaultBG:
		return "\x1b[49m"
	case c < 8:
		return fmt.Sprintf("\x1b[4%dm", c)
	case c < 16:
		return fmt.Sprintf("\x1b[10%dm", c-8)
	case c < 256:
		return fmt.Sprintf("\x1b[48;5;%dm", c)
	}
	return "\x1b[49m"
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
