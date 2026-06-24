package tui

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/micro-editor/terminal"
)

// privateCSIPattern matches CSI sequences with a private-parameter prefix of
// `<`, `>`, or `=`. micro-editor/terminal only recognizes `?` as a private
// marker; any sequence starting with one of the others falls through to the
// final-byte switch with no args, so e.g. `\e[<u` (Kitty keyboard push) gets
// dispatched as `case 'u'` → DECRC → cursor jumps to the saved position.
//
// Claude Code uses these for the Kitty keyboard protocol. Filtering them
// out is correct as long as we don't implement those protocols ourselves —
// the emulator can't honor the request anyway. Real fix is upstream.
var privateCSIPattern = regexp.MustCompile("\x1b\\[[<>=][0-9;]*[a-zA-Z]")

// sgrPattern matches an SGR ("set graphics rendition") sequence:
// CSI <params> m where params are digits and semicolons.
var sgrPattern = regexp.MustCompile(`\x1b\[([0-9;]*)m`)

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
//
// Two pre-processing passes before the emulator sees the bytes:
//   - Strip private-marker CSI (<, >, =) — emulator misdispatches them.
//   - Downsample 24-bit RGB SGR (38;2;R;G;B / 48;2;R;G;B) to nearest
//     256-color (38;5;N / 48;5;N). Color type in micro-editor/terminal
//     is uint16, so 24-bit isn't representable; without this, the RGB
//     params leak through as separate SGR codes and corrupt state.
func (t *SessionTerminal) Feed(b []byte) {
	if len(b) == 0 {
		return
	}
	b = privateCSIPattern.ReplaceAll(b, nil)
	b = downsampleTrueColor(b)
	_, _ = t.vt.Write(b)
}

// downsampleTrueColor walks every SGR sequence and rewrites any
// 38;2;R;G;B / 48;2;R;G;B triples to their nearest 38;5;N / 48;5;N
// approximation. Other params pass through. Compound sequences like
// "1;38;2;R;G;B;3" are handled (each param replaced in place).
func downsampleTrueColor(b []byte) []byte {
	return sgrPattern.ReplaceAllFunc(b, func(match []byte) []byte {
		// match looks like "\x1b[<params>m"; extract params.
		// sgrPattern guarantees the prefix and suffix.
		params := match[2 : len(match)-1]
		if !bytes.Contains(params, []byte("2")) {
			return match // no possible truecolor triple
		}
		parts := splitParams(params)
		out := parts[:0:len(parts)]
		for i := 0; i < len(parts); i++ {
			p := parts[i]
			// Detect 38;2;R;G;B or 48;2;R;G;B.
			if (p == "38" || p == "48") && i+4 < len(parts) && parts[i+1] == "2" {
				r, errR := atoi(parts[i+2])
				g, errG := atoi(parts[i+3])
				bl, errB := atoi(parts[i+4])
				if errR == nil && errG == nil && errB == nil {
					n := rgbTo256(r, g, bl)
					out = append(out, p, "5", itoa(n))
					i += 4
					continue
				}
			}
			out = append(out, p)
		}
		return []byte("\x1b[" + joinSemis(out) + "m")
	})
}

// rgbTo256 maps an 8-bit RGB triple to the closest xterm 256-color index.
// 0–15 are reserved for ANSI; we draw from the 6×6×6 cube (16–231) and
// the 24-step grayscale ramp (232–255).
func rgbTo256(r, g, b int) int {
	// Grayscale path: when R≈G≈B and not at the extremes, the ramp is
	// closer than the cube.
	if abs(r-g) < 8 && abs(g-b) < 8 {
		// Map average 0..255 to 0..23 grayscale.
		avg := (r + g + b) / 3
		if avg < 8 {
			return 16 // black, also covers DefaultFG fallback
		}
		if avg > 248 {
			return 231
		}
		return 232 + (avg-8)/10
	}
	// 6x6x6 cube. The xterm cube uses non-linear stops at 0, 95, 135,
	// 175, 215, 255. Quantize each channel to the nearest stop.
	return 16 + 36*cube6(r) + 6*cube6(g) + cube6(b)
}

func cube6(v int) int {
	stops := [6]int{0, 95, 135, 175, 215, 255}
	best, bestD := 0, 1 << 30
	for i, s := range stops {
		d := v - s
		if d < 0 {
			d = -d
		}
		if d < bestD {
			best, bestD = i, d
		}
	}
	return best
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// Tiny helpers — avoid pulling in strconv per-byte.
func atoi(s string) (int, error) {
	n := 0
	if len(s) == 0 {
		return 0, errEmpty
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errBadDigit
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [4]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func splitParams(b []byte) []string {
	if len(b) == 0 {
		return []string{""}
	}
	parts := []string{}
	last := 0
	for i, c := range b {
		if c == ';' {
			parts = append(parts, string(b[last:i]))
			last = i + 1
		}
	}
	parts = append(parts, string(b[last:]))
	return parts
}

func joinSemis(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	n := len(parts) - 1
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for i, p := range parts {
		if i > 0 {
			out = append(out, ';')
		}
		out = append(out, p...)
	}
	return string(out)
}

var (
	errEmpty    = parseErr("empty")
	errBadDigit = parseErr("bad digit")
)

type parseErr string

func (e parseErr) Error() string { return string(e) }

// Resize updates the virtual terminal dimensions. The agent's PTY should be
// resized to match — see Workspace.resizeAllSessions.
func (t *SessionTerminal) Resize(cols, rows int) {
	cols = clampMin(cols, 20)
	rows = clampMin(rows, 5)
	t.cols, t.rows = cols, rows
	t.vt.Resize(cols, rows)
}

// Render walks the cell grid and returns the screen with ANSI color codes
// embedded per cell. Tracks the previously emitted fg/bg so we only write
// new SGR sequences when a cell's color differs from its predecessor — keeps
// the output reasonably compact even on full-color screens. Resets at the
// end so the focused pane border doesn't pick up the last cell's colors.
func (t *SessionTerminal) Render() string {
	t.state.Lock()
	defer t.state.Unlock()

	var sb strings.Builder
	// Sentinels we'll never match on the first cell, forcing both codes
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

// defaultBgSGR paints cells the agent leaves at its default background with
// the workspace slate (#22222e = rgb 34,34,46, must match workspaceBg) so
// live output blends into the pane instead of showing stark terminal black.
const defaultBgSGR = "\x1b[48;2;34;34;46m"

// bgCode is the background equivalent of fgCode.
func bgCode(c terminal.Color) string {
	switch {
	case c == terminal.DefaultBG:
		return defaultBgSGR
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

// MouseMode reports whether the program running in this terminal has asked
// for mouse reporting, and whether it wants SGR (1006) encoding. Used to
// decide if forwarding a mouse event is safe — sending mouse bytes to a
// program that didn't enable mouse mode (e.g. a bare shell) would land as
// garbage on its input.
func (t *SessionTerminal) MouseMode() (enabled, sgr bool) {
	t.state.Lock()
	defer t.state.Unlock()
	return t.state.Mode(terminal.ModeMouseMask), t.state.Mode(terminal.ModeMouseSgr)
}

func clampMin(n, lo int) int {
	if n < lo {
		return lo
	}
	return n
}
