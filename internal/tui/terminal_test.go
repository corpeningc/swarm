package tui

import (
	"strings"
	"testing"
)

func TestSessionTerminal_PlainText(t *testing.T) {
	st := NewSessionTerminal(80, 24)
	st.Feed([]byte("hello world\r\n"))
	got := st.Render()
	if !strings.Contains(got, "hello world") {
		t.Errorf("plain text not rendered; got %q", got)
	}
}

func TestSessionTerminal_StripsANSI(t *testing.T) {
	st := NewSessionTerminal(80, 24)
	st.Feed([]byte("\x1b[31mred\x1b[0m text\r\n"))
	got := st.Render()
	if !strings.Contains(got, "red text") {
		t.Errorf("ANSI-bracketed text not preserved; got %q", got)
	}
	if strings.Contains(got, "\x1b[") {
		t.Errorf("escape sequences leaked through; got %q", got)
	}
}

func TestSessionTerminal_CursorMoves(t *testing.T) {
	st := NewSessionTerminal(20, 5)
	// Write 'aaa', then move cursor home, overwrite with 'b': result should
	// have 'baa' at the top — proving the emulator interprets cursor moves.
	st.Feed([]byte("aaa\x1b[Hb"))
	got := st.Render()
	first := strings.SplitN(got, "\n", 2)[0]
	if !strings.HasPrefix(first, "baa") {
		t.Errorf("cursor move not honored; first line = %q (full = %q)", first, got)
	}
}

func TestSessionTerminal_ResizePreservesContent(t *testing.T) {
	st := NewSessionTerminal(80, 24)
	st.Feed([]byte("persistent\r\n"))
	st.Resize(60, 20)
	if c, r := st.Size(); c != 60 || r != 20 {
		t.Errorf("Size after Resize = %dx%d, want 60x20", c, r)
	}
	if !strings.Contains(st.Render(), "persistent") {
		t.Errorf("content lost on resize")
	}
}

func TestSessionTerminal_TinySizeClamped(t *testing.T) {
	// Don't crash on early-render 0x0 windows.
	st := NewSessionTerminal(0, 0)
	st.Feed([]byte("ok\r\n"))
	if st.Render() == "" {
		t.Errorf("clamped terminal produced empty render")
	}
}

func TestSessionTerminal_AltScreenStripped(t *testing.T) {
	st := NewSessionTerminal(40, 5)
	// Sequence Claude actually emits on launch: enter alt screen, render
	// content, exit alt. With the filter, the content paints on main and
	// Render shows it.
	st.Feed([]byte("\x1b[?1049hHello from alt\x1b[?1049l"))
	got := st.Render()
	if !strings.Contains(got, "Hello from alt") {
		t.Errorf("alt-screen content lost; got %q", got)
	}
	if strings.Contains(got, "1049") {
		t.Errorf("escape sequence digits leaked through; got %q", got)
	}
}

func TestSessionTerminal_AltScreenAllVariants(t *testing.T) {
	cases := []string{
		"\x1b[?47h",
		"\x1b[?47l",
		"\x1b[?1047h",
		"\x1b[?1047l",
		"\x1b[?1048h",
		"\x1b[?1048l",
		"\x1b[?1049h",
		"\x1b[?1049l",
	}
	for _, code := range cases {
		st := NewSessionTerminal(40, 3)
		st.Feed([]byte(code + "marker"))
		got := st.Render()
		if !strings.Contains(got, "marker") {
			t.Errorf("filter dropped content for %q; got %q", code, got)
		}
	}
}
