//go:build !windows

package ptyutil

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aymanbagabas/go-pty"
)

// Drive the joiner from a real pseudo-terminal, the way the adapters do, so
// the platform's actual read chunking is what gets exercised. The child
// prints one long line of 3-byte glyphs; on macOS the kernel returns it in
// 1024-byte reads and tears a glyph at nearly every boundary.
func TestRuneJoinerRealPTY(t *testing.T) {
	pt, err := pty.New()
	if err != nil {
		t.Fatalf("pty.New: %v", err)
	}
	defer pt.Close()

	line := strings.Repeat("─", 2000) + strings.Repeat("✻●│", 300)
	cmd := pt.Command("printf", "%s", line)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start printf: %v", err)
	}

	buf := make([]byte, 4096)
	var join RuneJoiner
	var joined []byte
	reads, torn := 0, 0
	for {
		n, err := pt.Read(buf)
		if n > 0 {
			reads++
			if !utf8.Valid(buf[:n]) {
				torn++
			}
			out := join.Feed(buf[:n])
			if !utf8.Valid(out) {
				t.Fatalf("read %d: emitted a torn character", reads)
			}
			joined = append(joined, out...)
		}
		if err != nil {
			break
		}
	}
	joined = append(joined, join.Flush()...)
	_ = cmd.Wait()

	// The line discipline appends \r\n; everything before it must be intact.
	if got := strings.TrimRight(string(joined), "\r\n"); got != line {
		t.Fatalf("output differs from what the child printed (%d vs %d bytes)", len(got), len(line))
	}
	t.Logf("%d reads, %d of them ended mid-character", reads, torn)
}
