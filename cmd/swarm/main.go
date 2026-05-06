package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/calebcorpening/swarm/internal/tui"
)

var version = "0.0.1-dev"

func main() {
	root := &cobra.Command{
		Use:   "swarm",
		Short: "Run multiple AI coding agents in parallel, each in its own git worktree.",
		RunE: func(cmd *cobra.Command, args []string) error {
			p := tea.NewProgram(tui.NewWorkspace(), tea.WithAltScreen())
			_, err := p.Run()
			return err
		},
	}

	root.AddCommand(
		&cobra.Command{
			Use:   "version",
			Short: "Print swarm version.",
			Run: func(cmd *cobra.Command, args []string) {
				fmt.Println(version)
			},
		},
		&cobra.Command{
			Use:   "doctor",
			Short: "Diagnose your environment (git, gh, agent CLIs, PTY).",
			RunE: func(cmd *cobra.Command, args []string) error {
				fmt.Println("doctor: not implemented yet")
				return nil
			},
		},
		&cobra.Command{
			Use:   "prune",
			Short: "Remove orphaned worktrees and stale session metadata.",
			RunE: func(cmd *cobra.Command, args []string) error {
				fmt.Println("prune: not implemented yet")
				return nil
			},
		},
		&cobra.Command{
			Use:   "run [tasks.yaml]",
			Short: "Execute a declarative task file (v0.2).",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return fmt.Errorf("declarative mode is planned for v0.2")
			},
		},
	)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
