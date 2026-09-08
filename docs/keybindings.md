# Terminal UI key bindings

The desktop app uses buttons and menus for all of this; these keys are for
`cmd/swarm`. A few carry over to the desktop app and are noted in the
[main README](../README.md).

**Idle (sidebar focused):**

| Key | Action |
|---|---|
| `n` | new session (modal) |
| `j` / `k` | navigate sessions |
| Enter | attach to focused session; on a restored session, resume + attach; on the Shell tab, attach its shell |
| Tab | cycle main pane: Preview -> Diff -> Shell |
| `m` | edit the repo's project memory |
| `d` | discard session - destroys worktree + branch (confirm) |
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
| Tab | cycle name -> prompt -> existing-worktree list |
| Ctrl+B | pick a different repo (directory picker) |
| Ctrl+A | switch agent backend (claude / codex / aider) |
| Ctrl+E | toggle global MCP servers for this session (default off) |
| Enter | submit; or pick highlighted worktree from list |
| Esc | cancel |

**Memory editor (`m` from idle):** Ctrl+S to save, Esc to cancel.

## MCP servers

Spawned sessions start with `--strict-mcp-config` so they don't boot your
globally-configured MCP servers - the dominant cost of session startup. Toggle
MCP back on per session with Ctrl+E when an agent actually needs those tools.

## Scrolling

Swarm launches Claude in its fullscreen renderer (`CLAUDE_CODE_NO_FLICKER=1`) so
it owns a fixed viewport and implements its own scrollback - classic mode relies
on the host terminal's scrollback, which a swarm pane doesn't have. Scroll the
conversation with the **mouse wheel** (swarm forwards it), or attach and use
Claude's keys (`PgUp`/`PgDn`, `Ctrl+O` transcript pager). In the Diff tab the
wheel / `Ctrl+D` / `Ctrl+U` scroll the diff. Enabling mouse capture disables the
host terminal's click-drag text selection while swarm runs.
