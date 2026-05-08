# Swarm

**Run multiple AI coding agents in parallel.** Each in its own git worktree. No tmux. First-class Windows. Per-repo memory that compounds across sessions.

<p align="center">
  <em>Demo: <a href="#demo">screen recording / asciinema below</a></em>
</p>

---

## Why

[claude-squad](https://github.com/smtg-ai/claude-squad) proved the category. It's been inactive for ~2 months and has open issues users are still hitting — Windows binaries that fail immediately, terminal output that breaks, "no more updates?" threads. Swarm is the actively-maintained successor.

What's different:

| | claude-squad | **Swarm** |
|---|---|---|
| Substrate | tmux | native PTY (Unix) + ConPTY (Windows) |
| Windows | bug-reported, broken | first-class, runs the same code path |
| Cross-agent | claude / codex / aider | claude (codex coming) |
| Diff review | session-level accept/reject | **file-level** accept/reject |
| Resume | none | `claude --resume` integration via SessionStart hook |
| Project memory | none | `.swarm/memory.md` injected per spawn |
| Maintenance | stalled | active |

## What it does

- **Spawn agents in isolated git worktrees.** Each session gets its own working tree under `.swarm/worktrees/<name>/`, so multiple agents can edit the same repo in parallel without stepping on each other.
- **Multi-repo from one window.** Sessions across different repositories show up in the same sidebar. Swap focus with `j`/`k`.
- **Attach / detach** any session. Press Enter to attach (your keystrokes go to the agent), Ctrl+Q to detach. Sessions keep running in the background while you work on others.
- **Awaiting-input indicator** powered by Claude's `Stop` and `Notification` hooks. The sidebar shows ◆ awaiting (yellow) when an agent paused for you, ● running (blue) when it's actively working. Falls back to a silence heuristic for older Claude versions.
- **File-level diff review.** Tab into the diff view, navigate files with `j`/`k`, mark each keep / discard with space. Accept commits the kept changes and ff-merges into your current branch.
- **`claude --resume` across restarts.** Swarm captures Claude's session UUID via the `SessionStart` hook on spawn. Reattaching to an existing worktree resumes the same conversation thread — full memory, no re-explaining.
- **Per-repo project memory.** Stable conventions live in `<repo>/.swarm/memory.md` (gitignored). Auto-injected as background context on every fresh spawn. Edit by hand or ask the agent to update it.
- **Persistent across reboots.** Session metadata at `~/.swarm/state.json` (atomic temp+rename writes). Worktrees stay on disk until you explicitly accept or discard.

## Install

Requires Go 1.25+, `git`, `claude` (Claude Code CLI). Optional: `gh` for PR-resolution flows.

```sh
go install github.com/corpeningc/swarm/cmd/swarm@latest
```

Or build from source:

```sh
git clone https://github.com/corpeningc/swarm
cd swarm
go build -o swarm ./cmd/swarm
./swarm
```

Make sure `$GOBIN` (default `~/go/bin`) is on your `PATH`.

## Quick start

```sh
cd ~/your-project
swarm
```

Press `n` to open the new-session modal. Type a name (slugified for the worktree dir; existing names re-attach), tab to the prompt field, type what the agent should do, hit enter. The session appears in the sidebar.

Press Enter on a focused session to **attach** — your keystrokes flow to the agent. Press Ctrl+Q to detach without killing it.

Press Tab to flip the main pane between live agent output and the worktree's `git diff`. In diff view, use `j`/`k` to navigate files and space to toggle each file between keep and discard.

Press `A` to accept the session — its commits ff-merge into your current branch and the worktree gets cleaned up. Press `d` to discard (with confirmation). Press `x` to kill the agent without destroying the worktree.

## Key bindings

**Idle (sidebar focused):**

| Key | Action |
|---|---|
| `n` | new session (modal) |
| `j` / `k` | navigate sessions |
| Enter | attach to focused session |
| Tab | flip diff ↔ live agent view |
| `A` | accept session into current branch |
| `d` | discard session (confirm) |
| `x` | kill agent |
| `q` | quit (confirm) |

**Attached (input goes to the agent):**

| Key | Action |
|---|---|
| Ctrl+Q | detach back to idle |
| _everything else_ | forwarded to the agent's PTY |

**Diff view (Tab from idle):**

| Key | Action |
|---|---|
| `j` / `k` | navigate files |
| Space | toggle keep/discard for the highlighted file |
| `r` | refresh diff snapshot |
| `A` | accept (uses per-file selection) |
| Tab | back to live view |

**New-session modal:**

| Key | Action |
|---|---|
| Tab | cycle name → prompt → existing-worktree list |
| Ctrl+B | pick a different repo (directory picker) |
| Enter | submit; or pick highlighted worktree from list |
| Esc | cancel |

## How it works (architecture)

`swarm` is a Bubbletea TUI that orchestrates one process per session. Each session:

1. **Worktree.** A `git worktree add` creates a detached HEAD checkout under `.swarm/worktrees/<name>/`. Already-existing names attach to the same worktree, allowing multi-day work.
2. **PTY.** `aymanbagabas/go-pty` opens a pseudo-terminal — `pty(7)` on Unix, ConPTY on Windows — with the agent process attached. There's no tmux in the loop; bytes flow directly through Go.
3. **VT.** Bytes from the agent feed `micro-editor/terminal`, a vt100/xterm emulator. The TUI walks its cell grid and emits ANSI per cell into the focused pane. Pre-processing strips Kitty keyboard CSI sequences (which the upstream parser misdispatches as DECRC) and downsamples 24-bit truecolor to 256-color (uint16 limit upstream).
4. **Hooks.** Spawning writes a `.claude/settings.local.json` into the worktree wiring `Stop` / `Notification` / `SessionStart` events to a hidden `swarm hook` subcommand. The subcommand drops a marker file (or, for `SessionStart`, the JSON payload including session_id) into `.swarm/hooks/<name>/`. The TUI's per-second tick reads those markers to drive awaiting-input state and `--resume` ID capture.
5. **State.** Sessions persist to `~/.swarm/state.json`. Restored sessions show as `interrupted` in the sidebar; the user can accept or discard them, or spawn a new agent into the same worktree (which will `--resume` the captured Claude session UUID).

The original design spec and the kitty-CSI / truecolor diagnostics are in `docs/` for reference.

## Status

**Working today:**

- macOS, Linux, Windows (ConPTY) PTY substrate
- Multi-repo session spawning with directory picker
- Attach / detach / file-level diff / accept-with-ff-merge
- Per-repo project memory injection
- Claude Code session-id capture and `--resume` on reattach
- Cross-platform `swarm prune` for orphan worktree cleanup
- Persistent session state across restarts

**Planned (see [ROADMAP.md](./ROADMAP.md)):**

- Worktree env-setup hook (`.swarm/setup.sh`) so fresh worktrees come pre-installed and pre-configured
- Codex adapter
- Cost tracking
- Templates / recipes (`swarm new --recipe pr-review 142`)
- Demo video, packaged installers (homebrew, scoop)

**Known limitations:**

- Some xterm features in the cell grid aren't preserved end-to-end (true 24-bit color downsamples to 256; bold/underline/italic attributes from `Cell()` aren't exposed by the upstream emulator yet — bullet-pointed in `docs/upstream-prs.md`).
- Memory injection grows the prompt prefix linearly. Curate the file periodically; future polish will add `swarm memory` for editor integration.

## Contributing

Issues and PRs welcome. Specific contribution opportunities are tracked in `docs/upstream-prs.md` (against `micro-editor/terminal`) and the issue tracker.

If you're filing a bug that involves the rendered TUI, the easiest way to capture a reproducer is to set `SWARM_DUMP_PTY=1` before launching — every byte read from the agent's PTY gets mirrored to `~/.swarm/dumps/<session>.log`. Attach that to your issue.

## License

MIT.
