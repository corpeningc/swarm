package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

// encodeWheel builds the terminal mouse sequence for a scroll-wheel event at
// 1-based cell (x, y), in SGR (1006) form when sgr is true, else legacy X10.
// Wheel-up is button code 64, wheel-down 65. Only used for programs that have
// enabled mouse reporting (see SessionTerminal.MouseMode).
func encodeWheel(up bool, sgr bool, x, y int) []byte {
	cb := 65 // wheel down
	if up {
		cb = 64
	}
	if x < 1 {
		x = 1
	}
	if y < 1 {
		y = 1
	}
	if sgr {
		// ESC [ < cb ; x ; y M
		return fmt.Appendf(nil, "\x1b[<%d;%d;%dM", cb, x, y)
	}
	// Legacy X10: ESC [ M, then three bytes each offset by 32.
	clamp := func(v int) byte {
		v += 32
		if v > 255 {
			v = 255
		}
		return byte(v)
	}
	return []byte{0x1b, '[', 'M', clamp(cb), clamp(x), clamp(y)}
}

// encodeKey translates a Bubbletea KeyMsg into the bytes a TTY-attached
// program would have read had the user typed the key directly. Used by
// attach mode to forward every keystroke to the focused agent's PTY.
//
// Not exhaustive — function keys, exotic Ctrl combos, and bracketed paste
// aren't handled. Covers what people actually press inside agent TUIs.
func encodeKey(k tea.KeyMsg) []byte {
	if k.Type == tea.KeyRunes {
		b := []byte(string(k.Runes))
		if k.Alt {
			return append([]byte{0x1b}, b...)
		}
		return b
	}

	out := encodeSpecial(k.Type)
	if out == nil {
		return nil
	}
	if k.Alt {
		return append([]byte{0x1b}, out...)
	}
	return out
}

func encodeSpecial(t tea.KeyType) []byte {
	switch t {
	case tea.KeyEnter:
		return []byte{'\r'}
	case tea.KeyTab:
		return []byte{'\t'}
	case tea.KeyEsc:
		return []byte{0x1b}
	case tea.KeyBackspace:
		return []byte{0x7f}
	case tea.KeyDelete:
		return []byte("\x1b[3~")
	case tea.KeySpace:
		return []byte{' '}
	case tea.KeyUp:
		return []byte("\x1b[A")
	case tea.KeyDown:
		return []byte("\x1b[B")
	case tea.KeyRight:
		return []byte("\x1b[C")
	case tea.KeyLeft:
		return []byte("\x1b[D")
	case tea.KeyHome:
		return []byte("\x1b[H")
	case tea.KeyEnd:
		return []byte("\x1b[F")
	case tea.KeyPgUp:
		return []byte("\x1b[5~")
	case tea.KeyPgDown:
		return []byte("\x1b[6~")
	case tea.KeyShiftTab:
		return []byte("\x1b[Z")
	case tea.KeyCtrlAt:
		return []byte{0x00}
	case tea.KeyCtrlA:
		return []byte{0x01}
	case tea.KeyCtrlB:
		return []byte{0x02}
	case tea.KeyCtrlC:
		return []byte{0x03}
	case tea.KeyCtrlD:
		return []byte{0x04}
	case tea.KeyCtrlE:
		return []byte{0x05}
	case tea.KeyCtrlF:
		return []byte{0x06}
	case tea.KeyCtrlG:
		return []byte{0x07}
	case tea.KeyCtrlH:
		return []byte{0x08}
	// Ctrl+I aliases to KeyTab (already handled), Ctrl+M to KeyEnter.
	case tea.KeyCtrlJ:
		return []byte{0x0a}
	case tea.KeyCtrlK:
		return []byte{0x0b}
	case tea.KeyCtrlL:
		return []byte{0x0c}
	case tea.KeyCtrlN:
		return []byte{0x0e}
	case tea.KeyCtrlO:
		return []byte{0x0f}
	case tea.KeyCtrlP:
		return []byte{0x10}
	// Ctrl+Q is reserved as the detach key; the workspace intercepts it.
	case tea.KeyCtrlR:
		return []byte{0x12}
	case tea.KeyCtrlS:
		return []byte{0x13}
	case tea.KeyCtrlT:
		return []byte{0x14}
	case tea.KeyCtrlU:
		return []byte{0x15}
	case tea.KeyCtrlV:
		return []byte{0x16}
	case tea.KeyCtrlW:
		return []byte{0x17}
	case tea.KeyCtrlX:
		return []byte{0x18}
	case tea.KeyCtrlY:
		return []byte{0x19}
	case tea.KeyCtrlZ:
		return []byte{0x1a}
	case tea.KeyCtrlBackslash:
		return []byte{0x1c}
	case tea.KeyCtrlCloseBracket:
		return []byte{0x1d}
	case tea.KeyCtrlCaret:
		return []byte{0x1e}
	case tea.KeyCtrlUnderscore:
		return []byte{0x1f}
	}
	return nil
}
