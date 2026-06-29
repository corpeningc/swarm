// Package claudecode is the agent adapter for Anthropic's `claude` CLI.
//
// The adapter spawns `claude` interactively under a PTY in the session's
// worktree directory, sends the initial prompt as a positional argument so
// Claude Code starts the conversation with the user's first turn, and exposes
// bidirectional IO via Send / Output for follow-up turns.
//
// Built on github.com/aymanbagabas/go-pty for cross-platform support
// (Unix PTY + Windows ConPTY) — the substrate that lets Swarm run on
// Windows without tmux.
package claudecode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aymanbagabas/go-pty"

	"github.com/corpeningc/swarm/internal/agent"
	"github.com/corpeningc/swarm/internal/agent/ptyutil"
)

const (
	// outputBuffer caps how many events we buffer before backpressure forces
	// the reader goroutine to wait. Generous enough that a normal TUI render
	// burst doesn't stall, small enough that a runaway agent can't OOM us.
	outputBuffer = 256

	// readChunkSize matches a typical TTY line buffer.
	readChunkSize = 4096

	// killGracePeriod is how long Kill waits for SIGINT to take effect before
	// escalating to SIGKILL + pty.Close.
	killGracePeriod = 2 * time.Second
)

// Binary is the executable name we shell out to. Override in tests.
var Binary = "claude"

// Adapter implements agent.Agent for Claude Code.
type Adapter struct {
	binary string

	mu      sync.Mutex
	cmd     *pty.Cmd
	pt      pty.Pty
	events  chan agent.Event
	done    chan struct{} // closed when readLoop exits
	killed  atomic.Bool
	spawned bool
}

// New constructs an adapter that will exec the given binary on Spawn. Pass
// an empty string to use the package-level Binary default.
func New(binary string) *Adapter {
	if binary == "" {
		binary = Binary
	}
	return &Adapter{
		binary: binary,
		events: make(chan agent.Event, outputBuffer),
		done:   make(chan struct{}),
	}
}

// Spawn launches claude under a PTY in opts.Cwd.
func (a *Adapter) Spawn(ctx context.Context, opts agent.SpawnOpts) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.spawned {
		return fmt.Errorf("claudecode: already spawned")
	}

	// Resolve the binary on PATH ourselves. go-pty on Windows joins a
	// relative Path with Dir (and skips real PATH lookup) when both are
	// set, which would make exec try `<worktree>/claude` instead of the
	// installed claude binary. exec.LookPath returns an absolute path on
	// every platform and on Windows automatically appends PATHEXT
	// extensions (so npm's claude.cmd shim resolves correctly).
	binPath, err := exec.LookPath(a.binary)
	if err != nil {
		return fmt.Errorf("claudecode: %s not found on PATH: %w", a.binary, err)
	}

	if opts.HooksDir != "" && opts.SessionID != "" {
		if err := writeClaudeHooks(opts.Cwd, opts.HooksDir, opts.SessionID); err != nil {
			return fmt.Errorf("claudecode: write hooks settings: %w", err)
		}
	}

	pt, err := pty.New()
	if err != nil {
		return fmt.Errorf("claudecode: pty.New: %w", err)
	}

	cmd := pt.CommandContext(ctx, binPath, buildArgs(opts)...)
	cmd.Dir = opts.Cwd
	cmd.Env = mergeEnv(opts.Env, opts.HooksDir)

	if err := cmd.Start(); err != nil {
		_ = pt.Close()
		return fmt.Errorf("claudecode: start %s: %w", a.binary, err)
	}

	var dump *os.File
	if opts.DumpPath != "" {
		dump, err = os.OpenFile(opts.DumpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			_ = pt.Close()
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			return fmt.Errorf("claudecode: open dump %s: %w", opts.DumpPath, err)
		}
	}

	a.cmd = cmd
	a.pt = pt
	a.spawned = true

	go a.readLoop(cmd, pt, dump)
	return nil
}

// Output returns the channel of agent events. Closes when the process exits.
func (a *Adapter) Output() <-chan agent.Event { return a.events }

// Resize informs the child process of a new terminal size. On Unix this maps
// to TIOCSWINSZ on the master fd; on Windows it resizes the ConPTY console
// buffer. Calls before Spawn return an error so callers don't silently lose
// their initial size.
func (a *Adapter) Resize(cols, rows int) error {
	a.mu.Lock()
	pt := a.pt
	a.mu.Unlock()
	if pt == nil {
		return fmt.Errorf("claudecode: not spawned")
	}
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("claudecode: invalid size %dx%d", cols, rows)
	}
	return pt.Resize(cols, rows)
}

// Send writes input verbatim to the agent's PTY master. No transformations
// — callers are responsible for line endings and any escape encoding. This
// keeps Send symmetric with raw terminal input so attach mode can forward
// keystrokes as bytes.
func (a *Adapter) Send(input string) error {
	a.mu.Lock()
	pt := a.pt
	a.mu.Unlock()
	if pt == nil {
		return fmt.Errorf("claudecode: not spawned")
	}
	_, err := io.WriteString(pt, input)
	return err
}

// Kill terminates the agent process and closes the PTY. Idempotent. Blocks
// until the read loop has fully drained.
func (a *Adapter) Kill() error {
	if !a.killed.CompareAndSwap(false, true) {
		<-a.done
		return nil
	}
	a.mu.Lock()
	cmd, pt := a.cmd, a.pt
	a.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}

	_ = cmd.Process.Signal(os.Interrupt)
	select {
	case <-a.done:
		return nil
	case <-time.After(killGracePeriod):
	}
	// Escalate to the whole process tree. Claude spawns children (MCP
	// servers, nested git) that survive a bare Process.Kill and keep file
	// handles open inside the worktree — on Windows that's what blocks the
	// subsequent `git worktree remove` / RemoveAll during discard.
	// ptyutil.KillProcessTree is platform-specific (taskkill /T on Windows,
	// process-group signal on Unix).
	ptyutil.KillProcessTree(cmd.Process)
	_ = cmd.Process.Kill()
	if pt != nil {
		_ = pt.Close()
	}
	<-a.done
	return nil
}

// readLoop streams the PTY into the events channel, then waits on the child
// to harvest its exit code, then closes both the events channel and the done
// signal. This goroutine owns cmd.Wait — Kill must not call it.
//
// dump, if non-nil, receives every byte read from the PTY before the chunk
// is forwarded as an event. We close it on exit.
func (a *Adapter) readLoop(cmd *pty.Cmd, pt pty.Pty, dump *os.File) {
	defer close(a.done)
	defer close(a.events)
	if dump != nil {
		defer dump.Close()
	}

	buf := make([]byte, readChunkSize)
	for {
		n, err := pt.Read(buf)
		if n > 0 {
			if dump != nil {
				_, _ = dump.Write(buf[:n])
			}
			a.events <- agent.Event{Kind: agent.EventOutput, Text: string(buf[:n])}
		}
		if err != nil {
			break
		}
	}

	exitCode := -1
	if waitErr := cmd.Wait(); waitErr == nil {
		exitCode = 0
	} else if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	a.events <- agent.Event{Kind: agent.EventDone, ExitCode: exitCode}
}

// buildArgs assembles the claude argv from SpawnOpts. The first positional
// arg (when set) is Claude Code's initial user message, so the session starts
// with the user's first turn already submitted.
func buildArgs(opts agent.SpawnOpts) []string {
	var args []string
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.SkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}
	if opts.ResumeID != "" {
		args = append(args, "--resume", opts.ResumeID)
	}
	if opts.StrictMCP {
		// Only load MCP servers passed via --mcp-config (none here), so the
		// session doesn't boot the user's global MCP servers on startup.
		args = append(args, "--strict-mcp-config")
	}
	if opts.AppendSystemPrompt != "" {
		args = append(args, "--append-system-prompt", opts.AppendSystemPrompt)
	}
	args = append(args, opts.ExtraArgs...)
	if opts.Prompt != "" {
		args = append(args, opts.Prompt)
	}
	return args
}

// mergeEnv overlays opts.Env on top of os.Environ, then pins TERM to a value
// our virtual terminal can actually emulate. Bubbletea may have set TERM to
// something exotic in the parent process; the agent should see plain
// xterm-256color so it doesn't try alt-screen quirks the emulator misses.
//
// hooksDir, when non-empty, is exposed as SWARM_HOOKS_DIR so the swarm
// hook subcommand (run by Claude's hook system) can find where to drop
// marker files.
func mergeEnv(extra map[string]string, hooksDir string) []string {
	base := os.Environ()
	if _, hasTerm := extra["TERM"]; !hasTerm {
		base = append(base, "TERM=xterm-256color")
	}
	if hooksDir != "" {
		base = append(base, "SWARM_HOOKS_DIR="+hooksDir)
	}
	// Force Claude's fullscreen (alt-screen) renderer. Its classic renderer
	// appends to the normal screen and relies on the host terminal's
	// scrollback for history — which a swarm pane doesn't have, so the
	// conversation can't be scrolled. Fullscreen owns a fixed viewport and
	// implements its own scrolling (mouse wheel, PgUp/PgDn, Ctrl+O), which
	// matches how swarm renders and is what makes scrollback work. The user
	// can override by setting CLAUDE_CODE_NO_FLICKER in their env.
	if _, ok := extra["CLAUDE_CODE_NO_FLICKER"]; !ok && os.Getenv("CLAUDE_CODE_NO_FLICKER") == "" {
		base = append(base, "CLAUDE_CODE_NO_FLICKER=1")
	}
	for k, v := range extra {
		base = append(base, k+"="+v)
	}
	return base
}

// writeClaudeHooks drops a .claude/settings.local.json into the worktree
// that wires Claude's Stop and Notification events to a hidden `swarm hook`
// subcommand. Each event invocation lands a marker file in <hooksDir>/<id>/
// that the parent swarm process polls to update session status without
// waiting on the silence heuristic.
//
// It also pins permission/worktree settings that prevent Claude from creating
// its own git worktree (see the cfg below), so its edits stay in the worktree
// swarm manages and remain visible in the diff tab.
//
// We intentionally write to settings.local.json rather than settings.json:
// it's claude's "personal-overrides" slot, gitignored by convention, and
// has highest precedence so our hooks don't need to merge with whatever
// the user's repo already declares.
func writeClaudeHooks(worktree, hooksDir, sessionID string) error {
	swarmBin, err := os.Executable()
	if err != nil {
		return err
	}
	mkHook := func(event string) []map[string]any {
		return []map[string]any{
			{
				"matcher": "",
				"hooks": []map[string]any{
					{
						"type": "command",
						// Args are quoted at the JSON level; the shell
						// claude invokes will split them.
						"command": fmt.Sprintf("%q hook %s %s", swarmBin, event, sessionID),
					},
				},
			},
		}
	}
	cfg := map[string]any{
		"hooks": map[string]any{
			"Stop":         mkHook("stop"),
			"Notification": mkHook("notify"),
			// SessionStart fires once at session creation (and on resume).
			// Its JSON payload includes session_id — captured by swarm
			// hook into <hooks>/<id>/session_start so the parent process
			// can persist it without scraping PTY output.
			"SessionStart": mkHook("session_start"),
		},
		// Claude is already running inside the worktree swarm created. Block
		// every path by which it would spin up its OWN worktree, since that
		// moves its edits into .claude/worktrees/<id> where swarm's diff tab
		// (git -C <.swarm/worktrees/id> diff) can't see them. Deny rules are
		// enforced even under --dangerously-skip-permissions: a bare tool name
		// strips the tool from Claude's context entirely.
		"permissions": map[string]any{
			"deny": []string{
				"EnterWorktree",            // direct worktree isolation tool
				"Agent(isolation:worktree)", // subagents requesting their own worktree
			},
		},
		// Keep background agents editing this working copy instead of
		// auto-isolating into .claude/worktrees/.
		"worktree": map[string]any{
			"bgIsolation": "none",
		},
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Join(worktree, ".claude")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "settings.local.json"), data, 0644)
}
