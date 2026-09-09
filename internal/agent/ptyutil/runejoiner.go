package ptyutil

import "unicode/utf8"

// RuneJoiner reassembles multi-byte UTF-8 characters that a PTY read cut in
// two. A read returns whatever the kernel has buffered — on macOS at most
// 1024 bytes at a time — with no regard for character boundaries, so a
// box-drawing glyph or emoji routinely straddles two reads. That's harmless
// for a byte-oriented consumer like the TUI's emulator, but the desktop app
// ships each chunk to the webview through JSON, which replaces the dangling
// bytes with U+FFFD: one glyph becomes two or three replacement cells, the
// line grows past the width the agent laid it out for, and Claude Code's
// incremental renderer then paints every later frame onto shifted cells.
//
// Feed returns the longest prefix of the carried bytes plus chunk that ends
// on a character boundary and holds back the rest, at most utf8.UTFMax-1
// bytes, for the next call. Only a well-formed lead byte and its
// continuation bytes are ever held; bytes that can't start a character pass
// straight through, so garbage never waits for a continuation that will not
// come. Flush hands back whatever is still held once the stream ends.
type RuneJoiner struct {
	carry []byte
}

// Feed returns the bytes of chunk (preceded by any held from the previous
// call) that form whole characters, holding back an incomplete trailing
// sequence. The result may alias chunk and is only valid until the next call.
func (j *RuneJoiner) Feed(chunk []byte) []byte {
	b := chunk
	if len(j.carry) > 0 {
		b = append(j.carry, chunk...)
		j.carry = nil
	}
	cut := len(b) - incompleteTail(b)
	if cut < len(b) {
		// Copy: the tail usually lives in the caller's read buffer, which
		// the next read overwrites.
		j.carry = append([]byte(nil), b[cut:]...)
	}
	return b[:cut]
}

// Flush returns and clears the held bytes. Call it when the stream ends so a
// character the agent never finished is still delivered rather than dropped.
func (j *RuneJoiner) Flush() []byte {
	out := j.carry
	j.carry = nil
	return out
}

// incompleteTail reports how many trailing bytes of b begin a multi-byte
// sequence whose remaining bytes haven't arrived: 0 when b ends on a
// character boundary or in bytes that no continuation could complete.
func incompleteTail(b []byte) int {
	// A character is at most utf8.UTFMax bytes, so only a lead byte within
	// the last UTFMax-1 positions can still be waiting on continuations.
	lo := max(0, len(b)-(utf8.UTFMax-1))
	for i := len(b) - 1; i >= lo; i-- {
		if !utf8.RuneStart(b[i]) {
			continue // continuation byte; its lead is further back
		}
		// FullRune treats an invalid encoding as complete (it decodes as
		// one error rune per byte), which is exactly the pass-through we
		// want for garbage — only a genuinely short sequence is held.
		if utf8.FullRune(b[i:]) {
			return 0
		}
		return len(b) - i
	}
	return 0
}
