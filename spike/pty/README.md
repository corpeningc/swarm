# PTY Spike

Validates the core architectural bet for Swarm: spawn an arbitrary interactive CLI under a PTY, wire stdin/stdout, no tmux.

## Run

```sh
go run .              # bash
go run . zsh
go run . claude       # the real test
```

If `claude` (or any agent CLI) renders cleanly, accepts input, and exits without corrupting the parent terminal, the no-tmux thesis holds on this platform.

## What this proves / doesn't prove

- ✅ Bidirectional IO works on macOS / Linux via `creack/pty`.
- ✅ Window resize propagates (SIGWINCH).
- ❓ Windows: `creack/pty` claims ConPTY support but needs to be tested on actual Windows. That's a separate spike on a Windows host.

## Next

If this spike works on macOS + Linux, build the same test on Windows (separate machine or VM). If `claude` renders cleanly under ConPTY, the architecture is green-lit.
