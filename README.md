# Swarm

> Run multiple AI coding agents in parallel, each isolated in its own git worktree.

Swarm is a single-binary CLI for managing parallel agent sessions (Claude Code, Codex, more coming). It is the actively-maintained successor to [claude-squad](https://github.com/smtg-ai/claude-squad), with no tmux dependency, first-class Windows support, file-level diff review, cost tracking, and best-of-N agent comparison.

**Status:** v0.0.1-dev. Not ready for use yet.

## Layout

- `cmd/swarm/` — CLI entry point.
- `internal/tui/` — Bubbletea workspace UI.
- `internal/session/` — session lifecycle.
- `internal/worktree/` — git worktree manager.
- `internal/agent/` — agent adapter interface.
- `internal/cost/` — token usage and cost aggregation.
- `internal/config/` — config + `SWARM_HOME` resolution.
- `internal/dag/` — declarative-mode scheduler (v0.2).
- `spike/` — throwaway architectural spikes.

## Build

```sh
go build -o swarm ./cmd/swarm
./swarm
```
