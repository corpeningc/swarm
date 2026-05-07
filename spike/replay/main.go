// replay feeds a captured PTY dump to the SessionTerminal a chunk at a time
// and reports cursor row at every escape boundary, so we can find where state
// diverges from where Claude expects it to be.
//
// Usage: go run . <dump-file> [cols] [rows]
package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"

	"github.com/micro-editor/terminal"
)

// Same filter we apply in SessionTerminal.Feed.
var privateCSIPattern = regexp.MustCompile("\x1b\\[[<>=][0-9;]*[a-zA-Z]")

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: replay <dump-file> [cols] [rows]")
		os.Exit(2)
	}
	cols, rows := 80, 24
	if len(os.Args) >= 3 {
		cols, _ = strconv.Atoi(os.Args[2])
	}
	if len(os.Args) >= 4 {
		rows, _ = strconv.Atoi(os.Args[3])
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	state := &terminal.State{}
	vt, _ := terminal.Create(state, io.NopCloser(bytes.NewReader(nil)))
	vt.Resize(cols, rows)

	// Walk the dump in escape-sized chunks. We split on ESC so each chunk
	// contains one escape (or a run of plain text). Print cursor position
	// after each chunk that ends in a row-affecting operation.
	chunks := splitOnEsc(data)
	totalOff := 0
	prevX, prevY := 0, 0
	for i, chunk := range chunks {
		if len(chunk) == 0 {
			continue
		}
		filtered := privateCSIPattern.ReplaceAll(chunk, nil)
		_, _ = vt.Write(filtered)

		state.Lock()
		x, y := state.Cursor()
		state.Unlock()

		dx, dy := x-prevX, y-prevY
		if dx != 0 || dy != 0 || rowAffecting(chunk) {
			fmt.Printf("[chunk %4d off=%6d] %s -> (col=%d row=%d) Δ=(%+d,%+d)\n",
				i, totalOff, render(chunk), x, y, dx, dy)
		}
		prevX, prevY = x, y
		totalOff += len(chunk)
	}

	fmt.Printf("\nFinal cursor: col=%d row=%d (rows=%d)\n", prevX, prevY, rows)
	fmt.Println("\n--- final screen ---")
	state.Lock()
	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			ch, _, _ := state.Cell(x, y)
			if ch == 0 {
				ch = ' '
			}
			fmt.Print(string(ch))
		}
		fmt.Println()
	}
	state.Unlock()
}

func splitOnEsc(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i := 0; i < len(data); i++ {
		if data[i] == 0x1b && i > start {
			out = append(out, data[start:i])
			start = i
		}
	}
	if start < len(data) {
		out = append(out, data[start:])
	}
	return out
}

// rowAffecting reports whether the chunk's bytes can move the cursor row.
func rowAffecting(b []byte) bool {
	for _, c := range b {
		if c == '\n' || c == '\v' || c == '\f' {
			return true
		}
	}
	if len(b) >= 3 && b[0] == 0x1b && b[1] == '[' {
		// Last byte is the CSI terminator. If it's a row-mover, return true.
		switch b[len(b)-1] {
		case 'A', 'B', 'E', 'F', 'H', 'd', 'r', 'M', 'L', 'S', 'T':
			return true
		}
	}
	if len(b) >= 2 && b[0] == 0x1b && (b[1] == 'D' || b[1] == 'M' || b[1] == '7' || b[1] == '8') {
		return true
	}
	return false
}

func render(b []byte) string {
	const max = 32
	var s []byte
	for i, c := range b {
		if i >= max {
			s = append(s, '.', '.', '.')
			break
		}
		switch {
		case c == 0x1b:
			s = append(s, []byte("\\e")...)
		case c == '\n':
			s = append(s, []byte("\\n")...)
		case c == '\r':
			s = append(s, []byte("\\r")...)
		case c == '\t':
			s = append(s, []byte("\\t")...)
		case c < 0x20:
			s = append(s, []byte(fmt.Sprintf("\\x%02x", c))...)
		case c >= 0x7f:
			s = append(s, []byte(fmt.Sprintf("\\x%02x", c))...)
		default:
			s = append(s, c)
		}
	}
	return string(s)
}
