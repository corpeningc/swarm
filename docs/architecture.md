# How swarm works

Two frontends - a Bubbletea TUI (`cmd/swarm`) and a Wails desktop app
(`desktop/`) - sit over one shared Go core (`internal/core`, `internal/agent`,
`internal/session`, `internal/worktree`, `internal/memory`). The PTY substrate,
worktree isolation and `claude --resume` plumbing are shared, not reimplemented.
See [desktop/README.md](../desktop/README.md) for the desktop-specific layer.

Each session is one process:

1. **Worktree.** `git worktree add -b swarm/<name>` creates a checkout on a
   fresh branch under `.swarm/worktrees/<name>/` - a real, pushable branch you
   integrate from in the Shell tab. Already-existing names attach to the same
   worktree, allowing multi-day work. The desktop app can also run a session
   in the repository's own working tree instead.
2. **PTY.** `aymanbagabas/go-pty` opens a pseudo-terminal - `pty(7)` on Unix,
   ConPTY on Windows - with the agent process attached. There's no tmux in the
   loop; bytes flow directly through Go.
3. **VT.** Bytes from the agent feed `micro-editor/terminal`, a vt100/xterm
   emulator. The TUI walks its cell grid and emits ANSI per cell into the
   focused pane; the desktop app streams the raw bytes to xterm.js instead.
   Pre-processing strips Kitty keyboard CSI sequences (which the upstream parser
   misdispatches as DECRC) and downsamples 24-bit truecolor to 256-color
   (uint16 limit upstream).
4. **Hooks.** Spawning writes a `.claude/settings.local.json` into the worktree
   wiring `Stop` / `Notification` / `SessionStart` events to a hidden
   `swarm hook` subcommand. The subcommand drops a marker file (or, for
   `SessionStart`, the JSON payload including session_id) into
   `.swarm/hooks/<name>/`. A per-second tick reads those markers to drive
   awaiting-input state and `--resume` ID capture.
5. **State.** Sessions persist to `~/.swarm/state.json` (atomic temp+rename
   writes). Restored sessions show as `interrupted`; relaunching the agent in
   its worktree `--resume`s the captured Claude session UUID when there is one.
   Worktrees stay on disk until you discard them or run `swarm prune`.

## Known limitations

- Some xterm features in the cell grid aren't preserved end-to-end (true 24-bit
  color downsamples to 256; bold/underline/italic attributes from `Cell()`
  aren't exposed by the upstream emulator yet - bullet-pointed in
  [upstream-prs.md](./upstream-prs.md)).
- Memory injection grows the prompt prefix linearly. Curate
  `.swarm/memory.md` periodically.

## Debugging

Set `SWARM_DUMP_PTY=1` before launching and every byte read from the agent's PTY
is mirrored to `~/.swarm/dumps/<session>.log`. Attach that to bug reports about
rendering.

The original design spec and the kitty-CSI / truecolor diagnostics live
alongside this file for reference.
