package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/spf13/cobra"

	"github.com/calebcorpening/swarm/internal/agent"
	"github.com/calebcorpening/swarm/internal/agent/claudecode"
	"github.com/calebcorpening/swarm/internal/config"
	"github.com/calebcorpening/swarm/internal/session"
	"github.com/calebcorpening/swarm/internal/tui"
	"github.com/calebcorpening/swarm/internal/worktree"
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
		AgentFactory:  func() agent.Agent { return claudecode.New("") },
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
	p := tea.NewProgram(ws, tea.WithAltScreen())
	_, err = p.Run()
	return err
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
	entries, err := os.ReadDir(swarmDir)
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Println("nothing to prune (.swarm/worktrees does not exist)")
		return nil
	}
	if err != nil {
		return err
	}

	g := worktree.NewGitManager()
	cleaned, failed := 0, 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(swarmDir, entry.Name())
		err := g.Destroy(ctx, &worktree.Worktree{ID: entry.Name(), Path: path, RepoRoot: repo})
		if err != nil {
			fmt.Fprintf(os.Stderr, "  failed: %s: %v\n", entry.Name(), err)
			failed++
			continue
		}
		fmt.Printf("  removed %s\n", entry.Name())
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
