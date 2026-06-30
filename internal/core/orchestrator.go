// Package core is the UI-agnostic orchestration layer for swarm sessions.
//
// It owns the spawn pipeline — create-or-attach a worktree, inject project
// memory, launch the agent, register the session — without any dependency on a
// particular frontend. The Bubbletea TUI and the Wails desktop app are both
// thin presentation layers over an Orchestrator.
//
// Concurrency: every method is safe to call from any goroutine. The underlying
// session.Registry is itself thread-safe; the slug/branch helpers are pure.
package core

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/corpeningc/swarm/internal/agent"
	"github.com/corpeningc/swarm/internal/config"
	"github.com/corpeningc/swarm/internal/memory"
	"github.com/corpeningc/swarm/internal/session"
	"github.com/corpeningc/swarm/internal/worktree"
)

// AgentFactory returns a fresh agent adapter per session, so each session owns
// its own PTY and lifecycle. Injected so alternate agents (codex, aider) and
// tests can swap in without touching the orchestrator.
type AgentFactory func() agent.Agent

// Deps is everything the orchestrator needs, supplied by the host process
// (cmd/swarm-desktop, the TUI, or a test).
type Deps struct {
	Registry       *session.Registry
	Git            worktree.Manager
	AgentFactories map[string]AgentFactory
	AgentNames     []string // display/selection order; [0] is the default
	ShellFactory   AgentFactory
	DefaultRepo    string
}

// Orchestrator drives session lifecycle on top of Deps.
type Orchestrator struct {
	deps Deps
}

// New returns an orchestrator over the given dependencies.
func New(deps Deps) *Orchestrator { return &Orchestrator{deps: deps} }

// Registry exposes the underlying session store for read access (List, Get).
func (o *Orchestrator) Registry() *session.Registry { return o.deps.Registry }

// AgentNames returns the configured agent selection order.
func (o *Orchestrator) AgentNames() []string { return o.deps.AgentNames }

// DefaultRepo returns the repo swarm was launched in, if any.
func (o *Orchestrator) DefaultRepo() string { return o.deps.DefaultRepo }

// agentFactory returns the constructor for the named agent, falling back to the
// default (AgentNames[0]) when the name is unknown or empty.
func (o *Orchestrator) agentFactory(name string) AgentFactory {
	if f, ok := o.deps.AgentFactories[name]; ok {
		return f
	}
	if len(o.deps.AgentNames) > 0 {
		return o.deps.AgentFactories[o.deps.AgentNames[0]]
	}
	return nil
}

// SpawnRequest is the input to Spawn — one per new session.
type SpawnRequest struct {
	Repo      string
	Prompt    string
	Name      string // optional user label; drives the branch and worktree dir
	AgentName string // claude, codex, aider; empty uses the default
	EnableMCP bool   // off by default — booting global MCP servers is the dominant startup cost
}

// isolatedWorktreeGuidance is appended to the spawned agent's system prompt so
// it knows it's already inside a swarm-managed worktree and shouldn't stack
// another one on top.
const isolatedWorktreeGuidance = "You are already running inside an isolated git worktree managed by swarm. " +
	"Work directly in the current directory. Do NOT create git worktrees or use worktree isolation for subagents."

// spawnTimeout bounds worktree creation + setup hook. The agent process itself
// keeps running past this — the timeout only covers the synchronous setup.
const spawnTimeout = 30 * time.Second

// Spawn creates-or-attaches a worktree for the request, injects project memory
// on a fresh conversation, launches the agent, and registers the session.
// Returns the live handle on success. Blocking — run it off the UI goroutine.
func (o *Orchestrator) Spawn(ctx context.Context, req SpawnRequest) (*session.Handle, error) {
	dirName := worktreeDirName(req.Name) // flat, filesystem-safe session ID
	branchName := branchNameFromLabel(req.Name)
	relPath := worktreeRelPath(req.Name) // nested dir mirroring the branch

	var resumeID, existingPath string
	if dirName == "" {
		// No name: generate a fresh, unambiguous one. Branch and dir match.
		dirName = o.deps.Registry.NextID()
		branchName = dirName
		relPath = dirName
	} else {
		// Reattaching to a known ID: inherit its captured Claude session id and
		// reuse its on-disk path. Refuse if a live session already owns it.
		for _, h := range o.deps.Registry.List() {
			if h.Session.ID != dirName || h.Worktree == nil {
				continue
			}
			if h.Agent != nil {
				return nil, fmt.Errorf("worktree %q is already in use by session %s", dirName, h.Session.Label())
			}
			if h.Session.ClaudeSessionID != "" {
				resumeID = h.Session.ClaudeSessionID
			}
			existingPath = h.Worktree.Path
		}
	}

	setupCtx, cancel := context.WithTimeout(ctx, spawnTimeout)
	defer cancel()

	wt, err := o.createOrAttachWorktree(setupCtx, req.Repo, dirName, relPath, branchName, existingPath)
	if err != nil {
		return nil, err
	}

	factory := o.agentFactory(req.AgentName)
	if factory == nil {
		return nil, fmt.Errorf("no agent factory configured")
	}
	a := factory()
	hooksDir := filepath.Join(req.Repo, ".swarm", "hooks")
	_ = os.MkdirAll(filepath.Join(hooksDir, dirName), 0755)

	// Inject project memory only on a fresh conversation; a resume already has
	// the prior context, so piling memory on would duplicate it.
	effectivePrompt := req.Prompt
	if resumeID == "" {
		effectivePrompt = memory.PromptWithMemory(req.Repo, req.Prompt)
	}

	opts := agent.SpawnOpts{
		Cwd:                wt.Path,
		Prompt:             effectivePrompt,
		SessionID:          dirName,
		HooksDir:           hooksDir,
		ResumeID:           resumeID,
		StrictMCP:          !req.EnableMCP,
		AppendSystemPrompt: isolatedWorktreeGuidance,
	}
	if os.Getenv("SWARM_DUMP_PTY") != "" {
		dumpDir := filepath.Join(config.Home(), "dumps")
		if mkErr := os.MkdirAll(dumpDir, 0755); mkErr == nil {
			opts.DumpPath = filepath.Join(dumpDir, dirName+".log")
		}
	}

	if err := a.Spawn(context.Background(), opts); err != nil {
		// Only clean up worktrees we just created (auto-id sessions); a reused
		// worktree existed before we touched it.
		if strings.HasPrefix(dirName, "sess-") {
			_ = o.deps.Git.Destroy(context.Background(), wt)
		}
		return nil, fmt.Errorf("spawn: %w", err)
	}

	now := time.Now()
	h := &session.Handle{
		Session: &session.Session{
			ID: dirName, Name: req.Name, RepoRoot: req.Repo, BaseRef: "HEAD",
			Branch: wt.Branch, Worktree: wt.Path, AgentName: req.AgentName,
			Prompt: req.Prompt, Status: session.StatusRunning,
			CreatedAt: now, UpdatedAt: now, ClaudeSessionID: resumeID,
		},
		Worktree: wt, Agent: a,
	}
	o.deps.Registry.Add(h)
	return h, nil
}

// createOrAttachWorktree reuses an existing worktree (by known path or by an
// on-disk dir at the nested path) or creates a fresh one. Mirrors the TUI's
// create-vs-attach decision so desktop and TUI sessions interoperate.
func (o *Orchestrator) createOrAttachWorktree(ctx context.Context, repo, dirName, relPath, branchName, existingPath string) (*worktree.Worktree, error) {
	path := existingPath
	if path == "" {
		path = filepath.Join(worktree.SwarmWorktreesDir(repo), filepath.FromSlash(relPath))
	}
	if _, statErr := os.Stat(path); statErr == nil {
		// Reuse: read the actual checked-out branch (the dir slug can't recover
		// a slashed branch name) and migrate legacy swarm/<slug> branches.
		branch := worktree.CurrentBranch(ctx, path)
		if branch == "" {
			branch = branchName
		}
		if reconcileLegacyBranch(ctx, path, branch, branchName) {
			branch = branchName
		}
		return &worktree.Worktree{
			ID: dirName, Path: path, BaseRef: "HEAD", Branch: branch, RepoRoot: repo,
		}, nil
	}

	wt, err := o.deps.Git.Create(ctx, repo, "HEAD", dirName, relPath, branchName)
	if err != nil {
		return nil, fmt.Errorf("worktree: %w", err)
	}
	// Run any .swarm/setup.{sh,ps1} before the agent starts; clean up a
	// half-prepared worktree if it fails.
	if setupErr := worktree.RunSetupHook(repo, wt.Path); setupErr != nil {
		_ = o.deps.Git.Destroy(context.Background(), wt)
		return nil, setupErr
	}
	return wt, nil
}

// Resume relaunches the agent for a restored/interrupted handle in its existing
// worktree, reusing the same session ID (so the registry replaces the dead
// handle in place). Resumes the Claude conversation when a session id was
// captured. Returns the fresh live handle.
func (o *Orchestrator) Resume(ctx context.Context, id string) (*session.Handle, error) {
	h, ok := o.deps.Registry.Get(id)
	if !ok {
		return nil, fmt.Errorf("resume: unknown session %q", id)
	}
	s := h.Session
	wt := h.Worktree
	if wt == nil {
		return nil, fmt.Errorf("resume: session has no worktree — discard it")
	}
	if _, err := os.Stat(wt.Path); err != nil {
		return nil, fmt.Errorf("resume: worktree is gone — discard it")
	}
	factory := o.agentFactory(s.AgentName)
	if factory == nil {
		return nil, fmt.Errorf("resume: no agent factory configured")
	}
	a := factory()
	hooksDir := filepath.Join(s.RepoRoot, ".swarm", "hooks")
	_ = os.MkdirAll(filepath.Join(hooksDir, id), 0755)
	opts := agent.SpawnOpts{
		Cwd:                wt.Path,
		SessionID:          id,
		HooksDir:           hooksDir,
		ResumeID:           s.ClaudeSessionID,
		StrictMCP:          true,
		AppendSystemPrompt: isolatedWorktreeGuidance,
	}
	if err := a.Spawn(context.Background(), opts); err != nil {
		return nil, fmt.Errorf("resume: %w", err)
	}
	now := time.Now()
	resumed := &session.Handle{
		Session: &session.Session{
			ID: id, Name: s.Name, RepoRoot: s.RepoRoot, BaseRef: s.BaseRef, Branch: s.Branch,
			Worktree: wt.Path, AgentName: s.AgentName, Prompt: s.Prompt,
			Status: session.StatusRunning, CreatedAt: s.CreatedAt, UpdatedAt: now,
			ClaudeSessionID: s.ClaudeSessionID,
		},
		Worktree: wt, Agent: a,
	}
	o.deps.Registry.Add(resumed)
	return resumed, nil
}

// Kill terminates a session's agent. The worktree stays on disk for review or
// later discard. No-op for an already-dead (restored) handle beyond marking it.
func (o *Orchestrator) Kill(id string) error {
	h, ok := o.deps.Registry.Get(id)
	if !ok {
		return fmt.Errorf("kill: unknown session %q", id)
	}
	if h.Agent != nil {
		_ = h.Agent.Kill()
	}
	o.deps.Registry.SetStatus(id, session.StatusKilled)
	return nil
}

// Discard kills the agent, destroys the worktree and its branch, and removes
// the session from the registry. Irreversible.
func (o *Orchestrator) Discard(ctx context.Context, id string) error {
	h, ok := o.deps.Registry.Get(id)
	if !ok {
		return fmt.Errorf("discard: unknown session %q", id)
	}
	if h.Agent != nil {
		_ = h.Agent.Kill()
	}
	if h.Worktree != nil {
		if err := o.deps.Git.Destroy(ctx, h.Worktree); err != nil {
			// Remove the session anyway so the user isn't stuck; surface the
			// warning to the caller.
			o.deps.Registry.Remove(id)
			_ = os.RemoveAll(filepath.Join(h.Worktree.RepoRoot, ".swarm", "hooks", id))
			return fmt.Errorf("worktree not fully removed: %w", err)
		}
		_ = os.RemoveAll(filepath.Join(h.Worktree.RepoRoot, ".swarm", "hooks", id))
	}
	o.deps.Registry.Remove(id)
	return nil
}

// Diff returns `git -C <worktree> diff <baseRef>` for a session, covering both
// committed and uncommitted changes the agent made. colored controls whether
// ANSI color codes are embedded (TUI wants them; a webview renders its own).
func (o *Orchestrator) Diff(ctx context.Context, id string, colored bool) (string, error) {
	h, ok := o.deps.Registry.Get(id)
	if !ok || h.Worktree == nil {
		return "", fmt.Errorf("diff: unknown session %q", id)
	}
	baseRef := h.Session.BaseRef
	if baseRef == "" {
		baseRef = "HEAD"
	}
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	args := []string{"-C", h.Worktree.Path, "diff"}
	if colored {
		args = append(args, "--color=always")
	}
	args = append(args, baseRef)
	out, err := exec.CommandContext(dctx, "git", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("diff: %s", strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// SpawnShell launches an interactive shell in a session's worktree — the
// integration surface for git ops (commit, push, gh pr create). Returns the
// shell agent so the caller can stream/Send/Resize it. Works for restored
// sessions too, since the shell only needs the worktree, not a live agent.
func (o *Orchestrator) SpawnShell(ctx context.Context, id string) (agent.Agent, error) {
	h, ok := o.deps.Registry.Get(id)
	if !ok || h.Worktree == nil {
		return nil, fmt.Errorf("shell: session %q has no worktree", id)
	}
	if o.deps.ShellFactory == nil {
		return nil, fmt.Errorf("shell: no shell factory configured")
	}
	sh := o.deps.ShellFactory()
	if err := sh.Spawn(ctx, agent.SpawnOpts{Cwd: h.Worktree.Path}); err != nil {
		return nil, fmt.Errorf("shell: %w", err)
	}
	return sh, nil
}
