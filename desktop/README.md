# swarm — desktop edition

A [Wails](https://wails.io) desktop frontend for swarm. It reuses the **exact
same Go core** as the terminal UI — `internal/core`, `internal/agent`,
`internal/session`, `internal/worktree`, `internal/memory` — and only swaps the
presentation layer. The PTY/ConPTY substrate, worktree isolation, and
`claude --resume` plumbing are shared, not reimplemented.

## Why a desktop app

Things a terminal can't give you that this unlocks:

- **Multiple live agents at once.** Toggle **▦ Grid** to tile every running
  agent's terminal — the parallel-agents pitch made literal. A TUI can only
  attach to one session at a time.
- **Rich diff review.** The Diff tab renders the worktree diff with proper
  scrolling and +/- coloring instead of a cramped pane.
- **Discoverability.** Buttons and a modal instead of memorized keybindings.

The terminal UI (`cmd/swarm`) remains the primary, SSH-friendly frontend. This
is a second frontend over the same engine, not a replacement.

## Architecture

```
            ┌─────────────────────────────┐
cmd/swarm ──▶  internal/core.Orchestrator  ◀── desktop (Wails)
 (TUI)      │  Spawn · Resume · Kill ·     │   app.go bindings
            │  Discard · Diff · Shell      │   + xterm.js frontend
            └──────────────┬──────────────┘
                           ▼
        agent · session · worktree · memory · config
```

- `internal/core/orchestrator.go` — UI-agnostic session lifecycle (the spawn
  pipeline lifted out of the Bubbletea `Workspace`).
- `desktop/app.go` — Wails bindings. Exported methods become JS promises; PTY
  output is streamed to the frontend as `pty:data` events.
- `desktop/frontend/` — Vite + xterm.js. One persistent `Terminal` per session
  so output accumulates while hidden; focus mode shows one, grid mode tiles all.

## Build & run

Requires Go 1.24+, Node 18+, the [Wails CLI](https://wails.io/docs/gettingstarted/installation)
(`go install github.com/wailsapp/wails/v2/cmd/wails@latest`), and (Windows)
WebView2 (ships with Win11).

```sh
cd desktop
wails dev      # hot-reload dev mode
wails build    # produces build/bin/swarm-desktop.exe
```

> `wails build` compiles the frontend (`npm install && npm run build`) before
> the Go binary, so `frontend/dist/` — required by the `//go:embed` in
> `main.go` — is generated as part of the build. A bare `go build ./desktop`
> only works after `frontend/dist/` exists.

The app launches in the directory you run it from; that git repo becomes the
default for new sessions. The New-session modal offers a type-ahead repo
dropdown (launch repo + repos of existing sessions + sibling repos) and a
**Browse…** button that opens the native folder picker.

## Keyboard

Two modes mirror the TUI. **Navigation** (default) drives the app; **attached**
routes every keystroke to the focused agent.

| Key | Action |
|---|---|
| `j` / `k` / `↑` / `↓` | move focus between sessions |
| `Enter` | attach to the focused session (resumes it if stopped) |
| `Ctrl+Q` | detach back to navigation |
| `n` | new session |
| `x` | kill agent (keep worktree) |
| `d` | discard session + worktree |
| `g` | toggle the grid of all live agents |
| `Tab` | cycle Terminal → Diff → Shell |
| `1` / `2` / `3` | jump to Terminal / Diff / Shell |

Clicking a terminal in single-pane view also attaches; in grid view a click
only selects, so you can click around without hijacking the keyboard.
