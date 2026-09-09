package ptyutil

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// Every way of cutting a string in two must yield two valid pieces that
// concatenate back to the original.
func TestRuneJoinerSplitAnywhere(t *testing.T) {
	inputs := []string{
		"plain ascii",
		"a─b",                  // 3-byte box drawing
		"é",                    // 2-byte
		"x😀y",                  // 4-byte emoji
		"─│╭╮✻●⎿❯",             // a run of 3-byte glyphs, as in a TUI frame
		"\x1b[38;5;4m─\x1b[0m", // escape sequences pass through untouched
	}
	for _, in := range inputs {
		for cut := 0; cut <= len(in); cut++ {
			var j RuneJoiner
			first := string(j.Feed([]byte(in[:cut])))
			second := string(j.Feed([]byte(in[cut:])))
			rest := string(j.Flush())
			if !utf8.ValidString(first) || !utf8.ValidString(second) {
				t.Errorf("%q cut at %d: torn character: %q / %q", in, cut, first, second)
			}
			if got := first + second + rest; got != in {
				t.Errorf("%q cut at %d: bytes changed: got %q", in, cut, got)
			}
			if rest != "" {
				t.Errorf("%q cut at %d: %q left over after a complete input", in, cut, rest)
			}
		}
	}
}

// A character can arrive one byte per read.
func TestRuneJoinerByteAtATime(t *testing.T) {
	const in = "😀─"
	var j RuneJoiner
	var out []byte
	for i := range len(in) {
		piece := j.Feed([]byte{in[i]})
		if !utf8.Valid(piece) {
			t.Fatalf("byte %d: emitted torn character %q", i, piece)
		}
		out = append(out, piece...)
	}
	if string(out) != in {
		t.Fatalf("got %q, want %q", out, in)
	}
}

// Bytes that can never be completed are not held back: an invalid byte, a
// stray continuation byte, or a lead byte followed by a byte outside its
// continuation range all pass straight through.
func TestRuneJoinerPassesGarbageThrough(t *testing.T) {
	cases := [][]byte{
		{'a', 0xff},
		{0x80, 0x80},
		{0xe2, 'x'},        // lead byte, then ASCII: can't be a character
		{0xed, 0xa0},       // encoded surrogate: FullRune calls it complete
		{0xe2, 0x94, 0x80}, // and a complete character stays complete
	}
	for _, in := range cases {
		var j RuneJoiner
		if got := j.Feed(in); !bytes.Equal(got, in) {
			t.Errorf("%x: held back %x", in, in[len(got):])
		}
	}
}

// Flush delivers a sequence the stream ended in the middle of.
func TestRuneJoinerFlush(t *testing.T) {
	var j RuneJoiner
	if got := j.Feed([]byte{0xe2, 0x94}); len(got) != 0 {
		t.Fatalf("emitted an incomplete character: %x", got)
	}
	if got := j.Flush(); !bytes.Equal(got, []byte{0xe2, 0x94}) {
		t.Fatalf("flush returned %x", got)
	}
	if got := j.Flush(); len(got) != 0 {
		t.Fatalf("second flush returned %x", got)
	}
}

// The held tail must survive the caller reusing its read buffer.
func TestRuneJoinerDoesNotAliasHeldBytes(t *testing.T) {
	var j RuneJoiner
	buf := []byte{'a', 0xe2, 0x94}
	_ = j.Feed(buf)
	copy(buf, "zzz") // the next read overwrites the buffer
	if got := j.Feed([]byte{0x80}); string(got) != "─" {
		t.Fatalf("got %q, want the box-drawing glyph", got)
	}
}

// The macOS case: the kernel hands PTY reads back 1024 bytes at a time, and
// 1024 is not a multiple of 3, so a frame full of box-drawing glyphs is torn
// at every read. The desktop app JSON-encodes each chunk for the webview,
// which turns every torn glyph into U+FFFD cells; through the joiner no
// chunk is ever torn, so nothing is substituted.
func TestRuneJoinerMacPTYChunks(t *testing.T) {
	const readSize = 1024
	frame := []byte(strings.Repeat("─", 2000))
	var j RuneJoiner
	var joined []byte
	torn := 0
	for off := 0; off < len(frame); off += readSize {
		chunk := frame[off:min(off+readSize, len(frame))]
		if !utf8.Valid(chunk) {
			torn++
			if !bytes.Contains(jsonEncode(chunk), []byte(fffdEscape)) {
				t.Fatalf("expected JSON to substitute U+FFFD for a torn chunk")
			}
		}
		out := j.Feed(chunk)
		if enc := jsonEncode(out); bytes.Contains(enc, []byte(fffdEscape)) {
			t.Fatalf("joined chunk at %d still torn: %q", off, enc)
		}
		joined = append(joined, out...)
	}
	joined = append(joined, j.Flush()...)
	if torn == 0 {
		t.Fatal("premise: raw 1024-byte reads should tear this frame")
	}
	if !bytes.Equal(joined, frame) {
		t.Fatal("joined output differs from the input")
	}
}

// fffdEscape is how encoding/json spells a byte of invalid UTF-8: the
// six-character escape, not the U+FFFD glyph itself.
const fffdEscape = "\\ufffd"

// jsonEncode is what Wails does to every event payload.
func jsonEncode(b []byte) []byte {
	enc, err := json.Marshal(string(b))
	if err != nil {
		panic(err)
	}
	return enc
}
