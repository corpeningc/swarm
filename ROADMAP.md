# Swarm — Roadmap

A living document. Updated as priorities shift. The original design spec (`swarm-design-spec-v2.docx`) was the launching point; this document supersedes it where they diverge.

## What Swarm is

A single-binary Go CLI for running multiple AI coding agent sessions in parallel — each in its own git worktree, each with a real environment ready to work in. No tmux. First-class Windows. Built to be the actively-maintained successor to `smtg-ai/claude-squad`.

## Differentiators (sharpened from user evidence)

Original spec proposed best-of-N comparison and replay/rewind as headline features. Both dropped after analyzing what `claude-squad` users actually request — neither appears in their tracker. The real signal:

1. **No tmux substrate.** Five of claude-squad's top-15 issues are tmux-rendering bugs (#216, #189, #137, #132, #243). Architecture choice validated by user pain. This is a *substrate*, not a *headline*.

2. **Worktree environment hook.** Issue #260 ("worktree environment setup hook — deps, env files, port isolation") is open, recent, and underserved. When you spawn an agent into a fresh worktree, dependencies aren't installed, `.env` files aren't copied, ports collide. **This is what stops agents from being useful in their first 30 seconds.** No competitor solves it. Compounds as users contribute setup recipes per language/framework. **The headline differentiator.**

3. **PR-native workflow.** `swarm new --pr 142` resolves the PR head, creates a worktree, opens a session preloaded with a structured review prompt. Issue #124 requested this and it's a natural fit for the agentic-review loop.

4. **Active maintenance.** Operational, not technical. Respond to issues within 48h, ship every 2-3 weeks. Erodes if I get distracted; the only insurance is sustained discipline.

## Status (10 commits on `main`)

Shipped:
- PTY spike validating no-tmux architecture on macOS
- Worktree manager (`git worktree` + `gh pr view`) with safety rails
- Claude Code adapter (interactive, bidirectional, kill-on-quit)
- Session registry, lifecycle, optional names
- Bubbletea workspace TUI with new-session modal, directory picker, multi-repo support
- Per-session VT emulator (micro-editor/terminal — chosen after vt10x failed alt-screen)
- Attach/detach mode (Enter to attach, Ctrl+Q to detach, F5 force-redraw)
- `swarm prune` for orphaned worktrees
- ANSI colors per cell (currently reverted, pending lipgloss interaction debug)

## Known issues (in priority order)

1. **Emulator drift over time.** After idle minutes in attach mode, Claude's TUI render desyncs — input appears at top, overwrites itself. F5 redraw is a stopgap. Real fix needs a captured byte stream that triggers the divergence, then locating the offending escape sequence. Plan: add `SWARM_DUMP_PTY=1` debug flag to write all PTY output to disk for postmortem.

2. **Colors don't render in the bordered pane.** Embedded SGR codes work in the raw `Render()` output (verified by tests) but lipgloss's `border.Render(content)` likely strips them. Fix: bypass lipgloss for the main pane and draw the frame manually.

3. **Windows ConPTY untested.** macOS half of the architecture is proven. Windows is the riskier half and the entire "no tmux" thesis stands or falls there. Needs a dedicated spike.

## Phase A — Daily-usable tool (next, ~3-4 sessions)

Without these, swarm is a one-way door — you can spawn sessions but can't finalize them. Required to use it for real work.

- [ ] `x` to kill a focused session (with confirmation if running)
- [ ] `a` to **accept** — apply the worktree's commits to the parent branch, then destroy the worktree
- [ ] `d` to **discard** — destroy the worktree without applying (with confirmation)
- [ ] Diff view: side pane showing `git diff <baseRef>` for the focused session
- [ ] `state.json` persistence: write on every status change, restore on launch (CS-212)

## Phase B — Differentiator features (~5-7 sessions)

The work that justifies the project existing. Headline features driving adoption.

- [ ] **Worktree env hook** — `.swarm/setup.sh` runs after `git worktree add`. Standard recipes shipped: copy listed gitignored files (`.env`, `.envrc`), run a setup command (`npm install`, `cargo build`), allocate unique localhost ports per session.
- [ ] **PR-native flow** — `swarm new --pr <num>` from CLI, plus a "from PR" affordance in the modal. Preload a review prompt template.
- [ ] Cost tracking — per-session tokens + USD, status bar aggregate. Scaffolded in `internal/cost`, needs wiring through agent events.
- [ ] File-level diff accept/reject — extends the Phase A diff view.

## Phase C — Launch prep (~4-6 sessions)

- [ ] Codex adapter — parity with claudecode (~80% same code).
- [ ] **Windows ConPTY spike** — must happen before launch, ideally earlier. De-risks the entire project.
- [ ] `swarm doctor` implementation — actual environment checks, status table.
- [ ] Demo video (60-90s): three sessions in parallel, env hook in action, PR review flow, Windows frame.
- [ ] README rewrite — lead with demo, position as claude-squad successor.
- [ ] Launch posts: HN ("Show HN: Swarm — ..."), r/programming, r/commandline, X.

## Future (v0.2+)

- Aider, OpenCode, Gemini CLI adapters
- Profile-based agent configs (`~/.swarm/config.yaml`)
- DAG / declarative mode (`swarm run tasks.yaml`)
- One-command import from claude-squad config and state
- Detach/reattach via daemon mode

## Permanently out of scope

- Web UI of any kind. Terminal-native, period.
- Cloud or hosted version. Local-first only.
- Building a coding agent — Swarm wraps existing agents.
- Best-of-N / replay-rewind — neither shows up in user research; cut from the differentiator list.

## Risks

| Risk | Likelihood | Mitigation |
|---|---|---|
| Windows ConPTY turns out harder than expected | Medium | Spike before Phase B; if intractable, document Windows as v0.2 |
| claude-squad maintainer reactivates and ships fast | Medium | Speed; the "env hook" differentiator is harder to retrofit on tmux-bound code |
| Anthropic ships native multi-session in Claude Code | Medium | Cross-agent support survives single-vendor solutions |
| Scope creep extends timeline | High (default) | Phase A has the hard backstop; B is best-effort |
| Solo maintenance burnout post-launch | Medium | Ship a real tool first, then worry about external contributions |

## Success metrics (revised)

- **Minimum**: ships within 6 weeks; author uses daily; 500 stars in 60 days
- **Strong**: 2,000 stars in 6 months; HN front page at launch; cited as the maintained alternative
- **Breakout**: passes claude-squad in stars within 12 months (OpenCode/Aider precedent)
