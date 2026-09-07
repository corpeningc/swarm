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

- **Spawn agents in isolated git worktrees.** Each session gets its own working tree under `.swarm/worktrees/<name>/`, so multiple agents can edit the same repo in parallel without stepping on each other. Or don't: the desktop app can also run a session in the repository's own working tree, on the branch you already have checked out.
- **Multi-repo from one window.** Sessions across different repositories show up in the same sidebar. Swap focus with `j`/`k`.
- **Attach / detach** any session. Press Enter to attach (your keystrokes go to the agent), Ctrl+Q to detach. Sessions keep running in the background while you work on others.
- **Attention routing.** The sidebar floats ◆ awaiting (yellow) sessions to the top, the window title shows how many need you, and a bell rings when one transitions — powered by Claude's `Stop` / `Notification` hooks, so you don't have to babysit the screen. A long silence heuristic is the fallback when hooks don't fire.
- **Tabbed main pane.** `Preview` (live agent output) · `Diff` (read-only review of the worktree vs its base) · `Shell` (an interactive shell *in* the worktree).
- **Integrate however you like.** Each session is a real `swarm/<name>` branch. Open the Shell tab to commit, push, `gh pr create`, rebase — whatever your normal git flow is. No opinionated merge step to fight.
- **`claude --resume` across restarts.** Swarm captures Claude's session UUID via the `SessionStart` hook on spawn. Reattaching to an existing worktree resumes the same conversation thread — full memory, no re-explaining.
- **Per-repo project memory.** Stable conventions live in `<repo>/.swarm/memory.md` (gitignored). Auto-injected as background context on every fresh spawn. Edit it in the TUI with `m`, by hand, or by asking the agent to update it.
- **Pluggable agents.** Claude Code is first-class (hooks, `--resume`); Codex and Aider are selectable per session with Ctrl+A in the new-session modal.
- **Worktree setup hook.** Drop a `.swarm/setup.{sh,ps1}` and swarm runs it in each fresh worktree before the agent starts — so `node_modules` and friends are ready.
- **Persistent across reboots.** Session metadata at `~/.swarm/state.json` (atomic temp+rename writes). Worktrees stay on disk until you discard them or `swarm prune`.

## Install

Both frontends need `git` and the [`claude`](https://claude.com/claude-code) CLI
on your `PATH` at runtime. Optional: `gh` for PR-resolution flows.

### Desktop app (recommended)

The GUI is the easiest way in — buttons and a modal instead of memorized
keybindings, plus a tiled grid of every live agent. Prebuilt, no toolchain
required: grab it from the [latest release](https://github.com/corpeningc/swarm/releases/latest).

| Platform | Get it |
|---|---|
| **Windows** | Download `swarm-desktop-<version>-windows-amd64-setup.exe` and run it. Installs WebView2 if missing, adds Start Menu + Desktop shortcuts, and registers an uninstaller. |
| **macOS** | Download `swarm-desktop-<version>-macos-universal.zip`, unzip, drag `swarm.app` to Applications. |
| **Linux** | Download `swarm-desktop-<version>-linux-amd64.tar.gz` and extract. Needs `libwebkit2gtk-4.1` and `libgtk-3`. |

Or one line:

```powershell
# Windows (PowerShell)
irm https://raw.githubusercontent.com/corpeningc/swarm/main/scripts/install.ps1 | iex
```

```sh
# macOS / Linux
curl -fsSL https://raw.githubusercontent.com/corpeningc/swarm/main/scripts/install.sh | sh
```

The scripts fetch the latest release, install it (Applications on macOS,
`~/.local/bin` plus a launcher entry on Linux), and clear the quarantine flag on
macOS. Pin a version with `-Version 0.1.0` / `SWARM_VERSION=0.1.0`.

> **The builds are unsigned.** Windows SmartScreen shows "Windows protected your
> PC" — click **More info → Run anyway**. macOS says the app "cannot be opened":
> right-click → **Open**, or run `xattr -dr com.apple.quarantine
> /Applications/swarm.app` (the install script does this for you).

### Terminal UI

Requires Go 1.25+.

```sh
go install github.com/corpeningc/swarm/cmd/swarm@latest
```

Make sure `$GOBIN` (default `~/go/bin`) is on your `PATH`.

### From source

```sh
git clone https://github.com/corpeningc/swarm
cd swarm

go build -o swarm ./cmd/swarm   # terminal UI
./swarm

cd desktop && wails build       # desktop app -> build/bin/swarm-desktop
```

Building the desktop app additionally needs Node 18+ and the
[Wails CLI](https://wails.io/docs/gettingstarted/installation); see
[desktop/README.md](desktop/README.md).

## Quick start

**Desktop app:** launch it from the Start Menu / Applications, click **New
session**, and pick your repo with **Browse…** — or launch it from inside a repo
(`swarm-desktop` in a project directory) and that repo becomes the default. The
modal's **Workspace** picker chooses where the agent runs (a fresh worktree, the
repo itself, or an existing worktree), and when that workspace has agent history
you can **continue an earlier conversation** instead of starting cold. From
there the keys below apply, plus **▦ Grid** to tile every live agent at once and
`Ctrl` `+`/`-` to scale the terminal text.

**Terminal UI:**

```sh
cd ~/your-project
swarm
```

Press `n` to open the new-session modal. Type a name (slugified for the worktree dir; existing names re-attach), tab to the prompt field, type what the agent should do, hit enter. The session appears in the sidebar.

Press Enter on a focused session to **attach** — your keystrokes flow to the agent (or to the shell, when the Shell tab is active). Press Ctrl+Q to detach without killing it.

Press Tab to cycle the main pane: **Preview** (live agent output) → **Diff** (read-only review of the worktree vs its base) → **Shell**. The Shell tab drops you into an interactive shell in the worktree on its `swarm/<name>` branch — commit, push, `gh pr create`, rebase, whatever your flow is.

**Scrolling:** swarm launches Claude in its fullscreen renderer (`CLAUDE_CODE_NO_FLICKER=1`) so it owns a fixed viewport and implements its own scrollback — classic mode relies on the host terminal's scrollback, which a swarm pane doesn't have. Scroll the conversation with the **mouse wheel** (swarm forwards it), or attach and use Claude's keys (`PgUp`/`PgDn`, `Ctrl+O` transcript pager). In the Diff tab the wheel / `Ctrl+D`/`Ctrl+U` scroll the diff. Enabling mouse capture disables the host terminal's click-drag text selection while swarm runs.

Press `d` to discard a session (with confirmation) — destroys the worktree and its branch. Press `x` to kill the agent without destroying the worktree.

## Key bindings

**Idle (sidebar focused):**

| Key | Action |
|---|---|
| `n` | new session (modal) |
| `j` / `k` | navigate sessions |
| Enter | attach to focused session; on a restored session, resume + attach; on the Shell tab, attach its shell |
| Tab | cycle main pane: Preview → Diff → Shell |
| `m` | edit the repo's project memory |
| `d` | discard session — destroys worktree + branch (confirm) |
| `x` | kill agent |
| `q` | quit (confirm) |

**Attached (input goes to the agent or shell):**

| Key | Action |
|---|---|
| Ctrl+Q | detach back to idle |
| _everything else_ | forwarded to the focused PTY |

**Diff view (read-only):**

| Key | Action |
|---|---|
| `j` / `k` | navigate files |
| Ctrl+D / Ctrl+U | scroll the diff content |
| `r` | refresh diff snapshot |
| Tab | next tab (Shell) |

**New-session modal:**

| Key | Action |
|---|---|
| Tab | cycle name → prompt → existing-worktree list |
| Ctrl+B | pick a different repo (directory picker) |
| Ctrl+A | switch agent backend (claude / codex / aider) |
| Ctrl+E | toggle global MCP servers for this session (default off) |
| Enter | submit; or pick highlighted worktree from list |
| Esc | cancel |

**Memory editor (`m` from idle):** Ctrl+S to save, Esc to cancel.

By default spawned sessions start with `--strict-mcp-config` so they don't boot your globally-configured MCP servers — the dominant cost of session startup. Toggle MCP back on per session with Ctrl+E when an agent actually needs those tools.

## How it works (architecture)

`swarm` is a Bubbletea TUI that orchestrates one process per session. Each session:

1. **Worktree.** A `git worktree add -b swarm/<name>` creates a checkout on a fresh branch under `.swarm/worktrees/<name>/` — a real, pushable branch you integrate from in the Shell tab. Already-existing names attach to the same worktree, allowing multi-day work.
2. **PTY.** `aymanbagabas/go-pty` opens a pseudo-terminal — `pty(7)` on Unix, ConPTY on Windows — with the agent process attached. There's no tmux in the loop; bytes flow directly through Go.
3. **VT.** Bytes from the agent feed `micro-editor/terminal`, a vt100/xterm emulator. The TUI walks its cell grid and emits ANSI per cell into the focused pane. Pre-processing strips Kitty keyboard CSI sequences (which the upstream parser misdispatches as DECRC) and downsamples 24-bit truecolor to 256-color (uint16 limit upstream).
4. **Hooks.** Spawning writes a `.claude/settings.local.json` into the worktree wiring `Stop` / `Notification` / `SessionStart` events to a hidden `swarm hook` subcommand. The subcommand drops a marker file (or, for `SessionStart`, the JSON payload including session_id) into `.swarm/hooks/<name>/`. The TUI's per-second tick reads those markers to drive awaiting-input state and `--resume` ID capture.
5. **State.** Sessions persist to `~/.swarm/state.json`. Restored sessions show as `interrupted` in the sidebar; press Enter to relaunch the agent in its worktree — `--resume`-ing the captured Claude session UUID when there is one — and attach in one step. Or discard them.

The original design spec and the kitty-CSI / truecolor diagnostics are in `docs/` for reference.

## Status

**Working today:**

- macOS, Linux, Windows (ConPTY) PTY substrate
- Multi-repo session spawning with directory picker
- Tabbed pane: live Preview / read-only Diff / interactive Shell in the worktree
- Branch-per-session (`swarm/<name>`) with git integration via the Shell tab
- Per-repo project memory injection + in-TUI editor (`m`)
- Pluggable agents: Claude Code (hooks + `--resume`), Codex, Aider
- Worktree env-setup hook (`.swarm/setup.{sh,ps1}`)
- Attention routing: awaiting sessions float up, window-title count, bell
- Cross-platform `swarm prune` for orphan worktree cleanup
- Persistent session state across restarts

**Planned (see [ROADMAP.md](./ROADMAP.md)):**

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
