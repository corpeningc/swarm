// Command swarm-desktop is the Wails desktop frontend for swarm. It reuses the
// same Go core (internal/core, agent, session, worktree, memory) as the TUI —
// only the presentation layer differs. The desktop shell unlocks what a
// terminal can't give you: multiple live agent panes at once and a rich,
// scrollable diff view.
package main

import (
	"context"
	"embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"

	"github.com/corpeningc/swarm/internal/agent"
	"github.com/corpeningc/swarm/internal/agent/claudecode"
	"github.com/corpeningc/swarm/internal/agent/genericcli"
	"github.com/corpeningc/swarm/internal/agent/shell"
	"github.com/corpeningc/swarm/internal/config"
	"github.com/corpeningc/swarm/internal/core"
	"github.com/corpeningc/swarm/internal/session"
	"github.com/corpeningc/swarm/internal/worktree"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	// Claude Code's hooks (wired by claudecode.writeClaudeHooks) invoke this
	// same binary as `swarm-desktop hook <event> <session-id>`, because the
	// hook command is os.Executable(). Handle that here and exit — without this
	// the Stop hook would relaunch the whole Wails GUI, which fails to start
	// outside a desktop context and surfaces as a stop-hook error.
	if len(os.Args) >= 4 && os.Args[1] == "hook" {
		_ = claudecode.RunHookMarker(os.Args[2], os.Args[3], os.Stdin)
		return
	}

	// Launched from Finder/Applications, macOS hands us a minimal PATH that
	// omits nvm/Homebrew/etc., so claude, node and shell tools would be
	// unresolvable. Adopt the login shell's PATH before anything spawns.
	config.FixLaunchPath()

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defaultRepo, _ := worktree.FindRepoRoot(ctx, cwd)
	if defaultRepo != "" {
		_ = worktree.EnsureGitignore(defaultRepo)
	}

	statePath := filepath.Join(config.Home(), "state.json")
	registry, restored, restoreErr := session.LoadOrNewRegistry(statePath)
	if restoreErr != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", restoreErr)
	}

	orch := core.New(core.Deps{
		Registry: registry,
		Git:      worktree.NewGitManager(),
		AgentFactories: map[string]core.AgentFactory{
			"claude": func() agent.Agent { return claudecode.New("") },
			"codex":  func() agent.Agent { return genericcli.New(genericcli.Codex()) },
			"aider":  func() agent.Agent { return genericcli.New(genericcli.Aider()) },
		},
		AgentNames:   []string{"claude", "codex", "aider"},
		ShellFactory: func() agent.Agent { return shell.New() },
		DefaultRepo:  defaultRepo,
	})

	app := NewApp(orch)
	if len(restored) > 0 {
		fmt.Fprintf(os.Stderr, "restored %d session(s) from %s\n", len(restored), statePath)
	}

	err = wails.Run(&options.App{
		Title:  "swarm",
		Width:  1280,
		Height: 820,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup: app.startup,
		Bind:      []any{app},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
