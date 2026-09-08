# Swarm

**Run multiple AI coding agents in parallel.** Each in its own git worktree. No
tmux. First-class Windows. Per-repo memory that compounds across sessions.

Swarm is the actively-maintained successor to
[claude-squad](https://github.com/smtg-ai/claude-squad): a native PTY/ConPTY
substrate instead of tmux, Windows that actually works, file-level diff review,
and `claude --resume` across restarts.

## Install

Needs `git` and the [`claude`](https://claude.com/claude-code) CLI on your
`PATH`. Optional: `gh` for PR-resolution flows.

### Desktop app (recommended)

Prebuilt, no toolchain required. Grab it from the
[latest release](https://github.com/corpeningc/swarm/releases/latest):

| Platform | File |
|---|---|
| **Windows** | `...-windows-amd64-setup.exe` - installs WebView2 if missing, adds shortcuts, registers an uninstaller |
| **macOS** | `...-macos-universal.zip` - unzip, drag `swarm.app` to Applications |
| **Linux** | `...-linux-amd64.tar.gz` - needs `libwebkit2gtk-4.1` and `libgtk-3` |

Or one line:

```powershell
irm https://raw.githubusercontent.com/corpeningc/swarm/main/scripts/install.ps1 | iex   # Windows
```

```sh
curl -fsSL https://raw.githubusercontent.com/corpeningc/swarm/main/scripts/install.sh | sh   # macOS / Linux
```

> **The builds are unsigned.** Windows SmartScreen: **More info -> Run anyway**.
> macOS: right-click -> **Open** (the install script clears the quarantine flag
> for you).

**Updating.** The app checks for a newer release on launch and shows a banner.
There's no auto-updater - re-run the installer (Windows upgrades in place,
reusing your install directory) or the one-line command above.

### Terminal UI

The same engine without the window - for a remote dev box, a container, or
anything else you reach over SSH. Requires Go 1.25+.

```sh
go install github.com/corpeningc/swarm/cmd/swarm@latest
```

Make sure `$GOBIN` (default `~/go/bin`) is on your `PATH`.

## Quick start

**Desktop app.** Launch it, click **New session**, pick your repo with
**Browse...**. The **Workspace** picker chooses where the agent runs - a fresh
worktree, the repo itself, or an existing worktree - and when that workspace has
agent history you can continue an earlier conversation instead of starting cold.
**Grid** tiles every live agent at once; `Ctrl` `+`/`-` scales the terminal text.

**Terminal UI.** Run `swarm` inside a repo, press `n`, name the session, type
what the agent should do, hit Enter. Enter attaches (keystrokes go to the
agent), Ctrl+Q detaches, Tab cycles Preview / Diff / Shell. Full key list in
[docs/keybindings.md](docs/keybindings.md).

## What you get

- **Isolated worktrees.** Each session gets `.swarm/worktrees/<name>/` on a real
  `swarm/<name>` branch, so parallel agents don't collide. Or run in the repo
  itself when you'd rather not branch.
- **Multi-repo from one window.** Sessions across different repositories share
  one sidebar.
- **Attention routing.** Sessions awaiting input float to the top and ring a
  bell, driven by Claude's `Stop` / `Notification` hooks - no babysitting.
- **Diff and Shell tabs.** Review the worktree against its base, then commit,
  push or `gh pr create` from a shell already in the right directory. No
  opinionated merge step to fight.
- **`claude --resume` across restarts.** Swarm captures Claude's session UUID on
  spawn, so reattaching resumes the same conversation thread.
- **Per-repo memory.** `<repo>/.swarm/memory.md` is injected as background
  context on every fresh spawn.
- **Pluggable agents.** Claude Code is first-class; Codex and Aider are
  selectable per session.
- **Setup hook.** Drop a `.swarm/setup.{sh,ps1}` and swarm runs it in each fresh
  worktree, so `node_modules` and friends are ready before the agent starts.

## Docs

- [Key bindings](docs/keybindings.md) - full TUI reference
- [Architecture](docs/architecture.md) - how sessions, PTYs and hooks fit together
- [Desktop app](desktop/README.md) - the Wails layer, and building it
- [Roadmap](ROADMAP.md) - what's planned

## Building from source

```sh
git clone https://github.com/corpeningc/swarm
cd swarm
go build -o swarm ./cmd/swarm   # terminal UI
cd desktop && wails build       # desktop app (needs Node 18+ and the Wails CLI)
```

## Contributing

Issues and PRs welcome. Upstream contribution opportunities against
`micro-editor/terminal` are tracked in
[docs/upstream-prs.md](docs/upstream-prs.md).

## License

MIT.
