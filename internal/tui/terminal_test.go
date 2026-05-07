package tui

import (
	"regexp"
	"strings"
	"testing"
)

// stripANSI lets tests assert on rendered text without caring about the
// embedded SGR codes Render now emits per cell.
var ansiStripPattern = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func stripANSI(s string) string { return ansiStripPattern.ReplaceAllString(s, "") }

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
	// output for the focused pane to actually display the color.
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

// TestSessionTerminal_KittyKeyboardCSIIgnored exercises the bug discovered
// in dumps from real Claude sessions: \e[<u is the Kitty keyboard "push
// flags" sequence, but the underlying emulator's parser treats `<` as a
// regular char, falls through to case 'u' (DECRC), and snaps the cursor
// to the last saved position. The filter must strip these sequences so
// they don't move the cursor.
func TestSessionTerminal_KittyKeyboardCSIIgnored(t *testing.T) {
	st := NewSessionTerminal(40, 5)
	// Walk the cursor down to row 3 so we can detect a jump back to home.
	st.Feed([]byte("hello\r\nworld\r\nthird\r\nfourth"))
	// Sequence Claude actually emits: kitty push, kitty pop, modifyOtherKeys.
	st.Feed([]byte("\x1b[<u\x1b[>1u\x1b[>4;2m"))
	st.Feed([]byte("AFTER"))
	got := st.Render()
	// "AFTER" must land on the last row we wrote text to (row 3, "fourth"),
	// not at the saved cursor position from emulator startup.
	lines := strings.Split(got, "\n")
	if len(lines) < 4 {
		t.Fatalf("not enough lines rendered: %d", len(lines))
	}
	if !strings.Contains(lines[3], "fourthAFTER") {
		t.Errorf("kitty CSI moved cursor; line 3 = %q (full = %q)", lines[3], got)
	}
}

// TestSessionTerminal_AltScreenSurvivesSwap exercises the property that
// motivated swapping emulators: enter alt screen, draw, exit alt screen,
// the original main-screen content should be visible again. vt10x failed
// this; micro-editor/terminal must pass it.
func TestRGBTo256_GrayMaps(t *testing.T) {
	cases := []struct {
		r, g, b, want int
	}{
		{0, 0, 0, 16},        // black
		{255, 255, 255, 231}, // white
		{128, 128, 128, 244}, // mid-gray ramp
	}
	for _, c := range cases {
		if got := rgbTo256(c.r, c.g, c.b); got != c.want {
			t.Errorf("rgbTo256(%d,%d,%d) = %d, want %d", c.r, c.g, c.b, got, c.want)
		}
	}
}

func TestRGBTo256_Cube(t *testing.T) {
	// Pure red, green, blue land on cube corners.
	if got := rgbTo256(255, 0, 0); got != 16+36*5 {
		t.Errorf("pure red = %d, want %d", got, 16+36*5)
	}
	if got := rgbTo256(0, 255, 0); got != 16+6*5 {
		t.Errorf("pure green = %d, want %d", got, 16+6*5)
	}
	if got := rgbTo256(0, 0, 255); got != 16+5 {
		t.Errorf("pure blue = %d, want %d", got, 16+5)
	}
}

func TestDownsampleTrueColor_RewritesRGB(t *testing.T) {
	in := []byte("\x1b[38;2;215;119;87mhello\x1b[0m")
	out := downsampleTrueColor(in)
	got := string(out)
	if !strings.Contains(got, "38;5;") {
		t.Errorf("expected downsampled fg; got %q", got)
	}
	if strings.Contains(got, "38;2;") {
		t.Errorf("truecolor sequence leaked through; got %q", got)
	}
	if !strings.Contains(got, "hello") {
		t.Errorf("payload lost; got %q", got)
	}
}

func TestDownsampleTrueColor_HandlesCompound(t *testing.T) {
	// Bold + RGB foreground + italic in one SGR.
	in := []byte("\x1b[1;38;2;200;200;200;3mtext\x1b[0m")
	out := downsampleTrueColor(in)
	got := string(out)
	if strings.Contains(got, "38;2;") {
		t.Errorf("truecolor not rewritten in compound SGR; got %q", got)
	}
	if !strings.Contains(got, "1;") || !strings.Contains(got, ";3m") {
		t.Errorf("non-RGB params not preserved; got %q", got)
	}
}

func TestDownsampleTrueColor_Background(t *testing.T) {
	in := []byte("\x1b[48;2;30;30;30mbg\x1b[0m")
	out := downsampleTrueColor(in)
	got := string(out)
	if !strings.Contains(got, "48;5;") {
		t.Errorf("bg not downsampled; got %q", got)
	}
	if strings.Contains(got, "48;2;") {
		t.Errorf("bg truecolor leaked; got %q", got)
	}
}

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
