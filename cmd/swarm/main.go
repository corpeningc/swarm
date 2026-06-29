package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/spf13/cobra"

	"github.com/corpeningc/swarm/internal/agent"
	"github.com/corpeningc/swarm/internal/agent/claudecode"
	"github.com/corpeningc/swarm/internal/agent/genericcli"
	"github.com/corpeningc/swarm/internal/agent/shell"
	"github.com/corpeningc/swarm/internal/config"
	"github.com/corpeningc/swarm/internal/session"
	"github.com/corpeningc/swarm/internal/tui"
	"github.com/corpeningc/swarm/internal/worktree"
)

var version = "0.0.1-dev"

func main() {
	root := &cobra.Command{
		Use:   "swarm",
		Short: "Run multiple AI coding agents in parallel, each in its own git worktree.",
		RunE:  runWorkspace,
	}

	root.AddCommand(
		&cobra.Command{
			Use:   "version",
			Short: "Print swarm version.",
			Run:   func(*cobra.Command, []string) { fmt.Println(version) },
		},
		&cobra.Command{
			Use:   "doctor",
			Short: "Diagnose your environment (git, gh, agent CLIs, PTY).",
			RunE: func(*cobra.Command, []string) error {
				fmt.Println("doctor: not implemented yet")
				return nil
			},
		},
		&cobra.Command{
			Use:   "prune",
			Short: "Remove orphaned worktrees and stale session metadata.",
			RunE:  runPrune,
		},
		&cobra.Command{
			Use:   "run [tasks.yaml]",
			Short: "Execute a declarative task file (v0.2).",
			Args:  cobra.ExactArgs(1),
			RunE: func(*cobra.Command, []string) error {
				return fmt.Errorf("declarative mode is planned for v0.2")
			},
		},
		&cobra.Command{
			Use:    "hook <event> <session-id>",
			Short:  "Internal: marker target for Claude Code hooks. Touches a file the parent swarm process polls.",
			Args:   cobra.ExactArgs(2),
			Hidden: true,
			RunE:   runHook,
		},
	)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runWorkspace(_ *cobra.Command, _ []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	// Default repo: the git root containing cwd, if there is one. If we
	// were launched outside any repo the user can still pick one with the
	// directory picker — they just don't get a fast-path default.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	defaultRepo, _ := worktree.FindRepoRoot(ctx, cwd)

	// First-run niceties: gitignore the .swarm/ dir so worktrees don't
	// leak into the user's commits.
	if defaultRepo != "" {
		_ = worktree.EnsureGitignore(defaultRepo)
	}

	pickerStart := defaultRepo
	if pickerStart != "" {
		pickerStart = filepath.Dir(pickerStart)
	} else {
		pickerStart = cwd
	}

	statePath := filepath.Join(config.Home(), "state.json")
	registry, restored, restoreErr := session.LoadOrNewRegistry(statePath)
	if restoreErr != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", restoreErr)
	}

	deps := tui.WorkspaceDeps{
		Registry:      registry,
		Git:           worktree.NewGitManager(),
		DefaultRepo:   defaultRepo,
		AgentFactories: map[string]tui.AgentFactory{
			"claude": func() agent.Agent { return claudecode.New("") },
			"codex":  func() agent.Agent { return genericcli.New(genericcli.Codex()) },
			"aider":  func() agent.Agent { return genericcli.New(genericcli.Aider()) },
		},
		AgentNames:    []string{"claude", "codex", "aider"},
		ShellFactory:  func() agent.Agent { return shell.New() },
		PickerStartIn: pickerStart,
	}

	// Force lipgloss to preserve embedded ANSI codes from our virtual
	// terminal rendering. Without this, lipgloss downgrades or strips
	// the SGR sequences we emit per cell, and the agent's UI shows up
	// monochrome inside the focused pane.
	lipgloss.SetColorProfile(termenv.TrueColor)

	ws := tui.NewWorkspace(deps)
	if len(restored) > 0 {
		fmt.Fprintf(os.Stderr, "restored %d session(s) from %s\n", len(restored), statePath)
	}
	// WithMouseCellMotion enables mouse reporting so the wheel can be
	// forwarded to the focused agent (it scrolls its own transcript). Note
	// this takes over the mouse, so terminal click-drag selection is
	// disabled while swarm runs.
	p := tea.NewProgram(ws, tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err = p.Run()
	return err
}

// runHook is invoked by Claude Code's hook system inside a spawned session.
// Writes a marker file whose existence the parent swarm process detects on
// its activity tick. If Claude piped a JSON payload on stdin (which it does
// for SessionStart, Stop, and Notification), the payload is the file's
// content; otherwise it's an empty marker. The parent process reads the
// JSON to extract details like the Claude session UUID.
//
// Reads the hooks directory from SWARM_HOOKS_DIR (set by the adapter when
// spawning). On any error or missing env we no-op silently — Claude doesn't
// care, and we don't want a failing hook to interrupt the agent's flow.
func runHook(_ *cobra.Command, args []string) error {
	hooksDir := os.Getenv("SWARM_HOOKS_DIR")
	if hooksDir == "" {
		return nil
	}
	event := args[0]
	sessionID := args[1]
	target := filepath.Join(hooksDir, sessionID, event)
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return nil
	}
	// Best-effort: read whatever Claude piped on stdin (~1KB JSON payload
	// for SessionStart). If reading fails or stdin is empty, we still
	// create an empty marker so the parent's existence check fires.
	payload, _ := io.ReadAll(os.Stdin)
	if err := os.WriteFile(target, payload, 0644); err != nil {
		return nil
	}
	return nil
}

// runPrune walks .swarm/worktrees/ in the enclosing repo and removes every
// session-style directory it finds. Falls back to RemoveAll when git refuses
// (orphan dir not registered as a worktree). Sweeps git's internal refs at
// the end with `git worktree prune`.
func runPrune(_ *cobra.Command, _ []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	repo, err := worktree.FindRepoRoot(ctx, cwd)
	if err != nil {
		return fmt.Errorf("swarm prune must run inside a git repo: %w", err)
	}

	swarmDir := worktree.SwarmWorktreesDir(repo)
	if _, statErr := os.Stat(swarmDir); errors.Is(statErr, fs.ErrNotExist) {
		fmt.Println("nothing to prune (.swarm/worktrees does not exist)")
		return nil
	}
	// Find leaf worktrees, which may be nested (h/56679-foo), not just
	// flat top-level dirs.
	rels := worktree.SwarmWorktreeRelPaths(repo)

	g := worktree.NewGitManager()
	cleaned, failed := 0, 0
	for _, rel := range rels {
		path := filepath.Join(swarmDir, filepath.FromSlash(rel))
		err := g.Destroy(ctx, &worktree.Worktree{ID: rel, Path: path, RepoRoot: repo})
		if err != nil {
			fmt.Fprintf(os.Stderr, "  failed: %s: %v\n", rel, err)
			failed++
			continue
		}
		fmt.Printf("  removed %s\n", rel)
		cleaned++
	}

	// Sweep git's internal worktree refs even if everything we removed was
	// already orphaned at the FS level.
	_ = exec.CommandContext(ctx, "git", "-C", repo, "worktree", "prune").Run()

	switch {
	case cleaned == 0 && failed == 0:
		fmt.Println("nothing to prune")
	case failed == 0:
		fmt.Printf("pruned %d worktree(s)\n", cleaned)
	default:
		fmt.Printf("pruned %d worktree(s); %d failed\n", cleaned, failed)
	}
	return nil
}
