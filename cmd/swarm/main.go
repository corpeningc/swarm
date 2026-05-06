package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/calebcorpening/swarm/internal/agent"
	"github.com/calebcorpening/swarm/internal/agent/claudecode"
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
			RunE: func(*cobra.Command, []string) error {
				fmt.Println("prune: not implemented yet")
				return nil
			},
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

	deps := tui.WorkspaceDeps{
		Registry:      session.NewRegistry(),
		Git:           worktree.NewGitManager(),
		DefaultRepo:   defaultRepo,
		AgentFactory:  func() agent.Agent { return claudecode.New("") },
		PickerStartIn: pickerStart,
	}

	p := tea.NewProgram(tui.NewWorkspace(deps), tea.WithAltScreen())
	_, err = p.Run()
	return err
}
