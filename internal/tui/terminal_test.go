package tui

import (
	"regexp"
	"strings"
	"testing"
)

// ansiStripPattern is for tests that want to assert on rendered text
// without caring about embedded SGR codes.
var ansiStripPattern = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func stripANSI(s string) string {
	return ansiStripPattern.ReplaceAllString(s, "")
}

func TestSessionTerminal_PlainText(t *testing.T) {
	st := NewSessionTerminal(80, 24)
	st.Feed([]byte("hello world\r\n"))
	if !strings.Contains(stripANSI(st.Render()), "hello world") {
		t.Errorf("plain text not rendered; got %q", st.Render())
	}
}

func TestSessionTerminal_PreservesANSIColors(t *testing.T) {
	st := NewSessionTerminal(80, 24)
	st.Feed([]byte("\x1b[31mred\x1b[0m text\r\n"))
	got := st.Render()
	if !strings.Contains(stripANSI(got), "red text") {
		t.Errorf("ANSI-bracketed text not preserved; got %q", got)
	}
	// Render re-emits color per cell, so the red SGR must appear in the
	// output for the actual TUI to display the color.
	if !strings.Contains(got, "\x1b[31m") {
		t.Errorf("expected red SGR code in render; got %q", got)
	}
}

func TestSessionTerminal_CursorMoves(t *testing.T) {
	st := NewSessionTerminal(20, 5)
	// Write 'aaa', move cursor home, overwrite with 'b': result should
	// start with 'baa' — proving the emulator interprets cursor moves.
	st.Feed([]byte("aaa\x1b[Hb"))
	first := strings.SplitN(stripANSI(st.Render()), "\n", 2)[0]
	if !strings.HasPrefix(first, "baa") {
		t.Errorf("cursor move not honored; first line = %q", first)
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

// TestSessionTerminal_AltScreenSurvivesSwap exercises the property that
// motivated swapping emulators: enter alt screen, draw, exit alt screen,
// the original main-screen content should be visible again. vt10x failed
// this; micro-editor/terminal must pass it.
func TestSessionTerminal_AltScreenSurvivesSwap(t *testing.T) {
	st := NewSessionTerminal(40, 5)
	// Paint on the main screen first.
	st.Feed([]byte("MAIN-content\r\n"))
	// Enter alt, draw, exit alt.
	st.Feed([]byte("\x1b[?1049hALT-content\x1b[?1049l"))
	got := st.Render()
	if !strings.Contains(got, "MAIN-content") {
		t.Errorf("alt-screen swap clobbered main-screen content; got %q", got)
	}
	if strings.Contains(got, "ALT-content") {
		t.Errorf("alt-screen content leaked into main-screen render; got %q", got)
	}
}
