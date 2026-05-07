# Upstream contribution opportunities

A running list of small, high-value patches Swarm has identified in its
dependencies. Each one is something we could file as an issue or PR while
working on Swarm itself. Keep entries scoped: one bug per entry, with the
reproducer that found it.

---

## micro-editor/terminal — recognize `<`, `>`, `=` as private CSI prefixes

**Repo:** `github.com/micro-editor/terminal`
**Status:** drafted, not filed
**Suggested PR title:** `csi: treat <, >, = as private-parameter prefixes (fixes Kitty keyboard misdispatch)`

### Bug

The CSI parser in `csi.go:parse()` only recognizes `?` as a private-parameter
prefix:

```go
if s[0] == '?' {
    c.priv = true
    s = s[1:]
}
```

CSI sequences that start with `<`, `>`, or `=` fall through this check.
Their prefix character ends up as the first byte of the args string,
`strconv.Atoi` fails on it, and the loop breaks with `args` empty.
`handleCSI` then dispatches the sequence by its terminator alone — without
the `priv` flag, and without knowing the prefix was non-standard.

This causes real misdispatches when a terminal client uses modern keyboard
protocols. Two examples observed in the wild:

| Sequence | Sent by                          | Misdispatched as            |
|----------|----------------------------------|-----------------------------|
| `\e[<u`  | Kitty keyboard "push flags"      | `case 'u'` → DECRC          |
| `\e[>1u` | Kitty keyboard "pop flags"       | `case 'u'` → DECRC          |
| `\e[>4;2m` | xterm modifyOtherKeys mode 2   | `case 'm'` → SGR reset      |

The DECRC misdispatches are particularly damaging: each one snaps the
cursor to the last DECSC-saved position. Once one happens, every
subsequent relative cursor move (`\e[NA`, `\e[NB`, etc.) is offset.

### Reproducer

Anthropic's `claude` CLI (Claude Code) uses these sequences during normal
operation. Running it under a host that uses this emulator (e.g. for a
multi-pane terminal) produces a visible cursor drift after a short typing
session — the input box ends up rendered ~20 rows above where the user
expects it.

A minimal Go reproducer:

```go
state := &terminal.State{}
vt, _ := terminal.Create(state, io.NopCloser(bytes.NewReader(nil)))
vt.Resize(80, 24)
vt.Write([]byte("\x1b7"))                // save cursor at (0,0)
vt.Write([]byte("foo\r\nbar\r\nbaz"))    // cursor now at (3, 2)
x, y := state.Cursor()
fmt.Println("before:", x, y)              // 3 2
vt.Write([]byte("\x1b[<u"))               // Kitty push — should be ignored
x, y = state.Cursor()
fmt.Println("after:", x, y)               // BUG: 0 0
```

### Proposed fix

Recognize the additional private-prefix bytes in `csi.go:parse`:

```go
// Treat ?, <, >, = as private-parameter prefixes per ECMA-48 / xterm.
// Sequences with these prefixes are parsed but should not be dispatched
// to the standard final-byte handlers.
switch s[0] {
case '?':
    c.priv = true
    s = s[1:]
case '<', '>', '=':
    c.priv = true
    s = s[1:]
}
```

Then in `handleCSI`, gate the cursor-restore and SGR cases on `!c.priv`
to avoid misdispatching kitty sequences:

```go
case 'm': // SGR
    if !c.priv {
        t.setAttr(c.args)
    }
case 'u': // DECRC (only when not a private sequence)
    if !c.priv {
        t.restoreCursor()
    }
```

The truly correct behavior would parse and honor the kitty keyboard
protocol, but that's a much larger feature. Treating private sequences
as no-ops is correct as long as the emulator doesn't claim to support
those protocols.

### Test to add

```go
func TestParseCSI_KittyPrivatePrefixIgnored(t *testing.T) {
    state := &State{}
    vt, _ := Create(state, io.NopCloser(bytes.NewReader(nil)))
    vt.Resize(80, 24)
    vt.Write([]byte("\x1b7"))             // save cursor at (0,0)
    vt.Write([]byte("foo\r\nbar"))        // cursor at (3, 1)
    vt.Write([]byte("\x1b[<u"))           // kitty push — must not move cursor
    x, y := state.Cursor()
    if x != 3 || y != 1 {
        t.Errorf("kitty CSI moved cursor: got (%d,%d), want (3,1)", x, y)
    }
}
```

### How Swarm worked around it locally

`internal/tui/terminal.go` strips the offending sequences in
`SessionTerminal.Feed` before they reach `vt.Write`. The local filter
becomes obsolete once this fix lands.

### Forensic notes

The bug was found by:
1. `SWARM_DUMP_PTY=1 ./swarm` — captures every byte read from the agent's PTY.
2. `spike/replay/main.go` — feeds the dump to a fresh emulator chunk-by-chunk
   and reports cursor position after each escape, so we can see when state
   diverges from where the agent expects it.
3. Grepped `\x1b\[[<>=]` in the dump → 8 occurrences in a single session
   → matched 1:1 with cursor-jump events in the replay output.

---

## micro-editor/terminal — 24-bit truecolor SGR support

**Repo:** `github.com/micro-editor/terminal`
**Status:** drafted, not filed
**Suggested PR title:** `state: handle 38;2;R;G;B and 48;2;R;G;B truecolor SGR`

### Bug

`Color` is `uint16` and `setAttr` only recognizes `38;5;N` / `48;5;N`
(256-color palette). Truecolor sequences like `\x1b[38;2;215;119;87m`
fall to the "gfx attr 38 unknown" log branch — the fg stays unchanged,
and the trailing R;G;B params get processed as separate SGR codes:
`2` does nothing, `215`/`119`/`87` are out of every defined SGR range.
But the iteration still walks them, so any non-RGB params interleaved
in a compound SGR (e.g. `1;38;2;R;G;B;3`) get processed in the wrong
context. Net effect: cells render at default fg, and emphasis state
leaks across writes.

This matters in practice because Claude Code uses truecolor
extensively for its themed UI (orange box-drawing, dim status, etc.).
Without 24-bit, the entire UI renders monochrome and looks broken.

### Reproducer

```go
state := &terminal.State{}
vt, _ := terminal.Create(state, io.NopCloser(bytes.NewReader(nil)))
vt.Resize(20, 5)
vt.Write([]byte("\x1b[38;2;215;119;87mhi"))
ch, fg, _ := state.Cell(0, 0)
fmt.Println(ch, fg) // 'h', DefaultFG — the truecolor was dropped
```

### Proposed fix

Extend `Color` to `uint32` so encodings have room for 24-bit RGB.
A clean encoding leaves 0–255 for ANSI/256-color, the existing
`0xff80`/`0xff81` sentinels intact, and uses bit 24 as the truecolor
flag: `0x1000000 | (R<<16) | (G<<8) | B`.

Add to `setAttr` (mirror for `case 48`):

```go
case 38:
    if i+2 < len(attr) && attr[i+1] == 5 {
        // existing 256-color path
    } else if i+4 < len(attr) && attr[i+1] == 2 {
        i += 4
        r, g, b := attr[i-2], attr[i-1], attr[i]
        if rgbValid(r) && rgbValid(g) && rgbValid(b) {
            t.cur.attr.fg = TrueColor(r, g, b)
        }
    }
```

Plus a `TrueColor(r, g, b int) Color` helper and an accessor so
callers can decode without knowing the bit layout.

### How Swarm worked around it locally

`internal/tui/terminal.go` pre-processes every SGR sequence in
`SessionTerminal.Feed` and rewrites any `38;2;R;G;B` / `48;2;R;G;B`
triples to their nearest `38;5;N` / `48;5;N` approximation. Quantization
uses the xterm 6×6×6 cube for non-gray and the 24-step grayscale ramp
for near-gray triples. Looks correct but slightly less saturated than
the source.

The downsampler is a clean fallback even after upstream support
lands — keeps Swarm working against older versions of the lib.
