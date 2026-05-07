package tui

import (
	"bytes"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestEncodeKey_Runes(t *testing.T) {
	got := encodeKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hi")})
	if !bytes.Equal(got, []byte("hi")) {
		t.Errorf("runes: got %q, want %q", got, "hi")
	}
}

func TestEncodeKey_AltPrefixesEsc(t *testing.T) {
	got := encodeKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a"), Alt: true})
	if !bytes.Equal(got, []byte{0x1b, 'a'}) {
		t.Errorf("alt+a: got %q", got)
	}
}

func TestEncodeKey_Specials(t *testing.T) {
	cases := []struct {
		name string
		in   tea.KeyType
		want []byte
	}{
		{"enter is CR", tea.KeyEnter, []byte{'\r'}},
		{"tab", tea.KeyTab, []byte{'\t'}},
		{"esc", tea.KeyEsc, []byte{0x1b}},
		{"backspace", tea.KeyBackspace, []byte{0x7f}},
		{"delete", tea.KeyDelete, []byte("\x1b[3~")},
		{"up", tea.KeyUp, []byte("\x1b[A")},
		{"down", tea.KeyDown, []byte("\x1b[B")},
		{"right", tea.KeyRight, []byte("\x1b[C")},
		{"left", tea.KeyLeft, []byte("\x1b[D")},
		{"ctrl+c", tea.KeyCtrlC, []byte{0x03}},
		{"ctrl+a", tea.KeyCtrlA, []byte{0x01}},
		{"ctrl+z", tea.KeyCtrlZ, []byte{0x1a}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := encodeKey(tea.KeyMsg{Type: tc.in})
			if !bytes.Equal(got, tc.want) {
				t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

func TestEncodeKey_CtrlQReserved(t *testing.T) {
	// Ctrl+Q is the detach key; encoder must return nil so a stray pass
	// to encodeKey can't accidentally forward it to the agent.
	if got := encodeKey(tea.KeyMsg{Type: tea.KeyCtrlQ}); got != nil {
		t.Errorf("ctrl+q should be nil (reserved for detach), got %q", got)
	}
}
