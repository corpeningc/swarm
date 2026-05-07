// Package claudecode is the agent adapter for Anthropic's `claude` CLI.
//
// The adapter spawns `claude` interactively under a PTY in the session's
// worktree directory, sends the initial prompt as a positional argument so
// Claude Code starts the conversation with the user's first turn, and exposes
// bidirectional IO via Send / Output for follow-up turns.
package claudecode

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/creack/pty"

	"github.com/calebcorpening/swarm/internal/agent"
)

const (
	// outputBuffer caps how many events we buffer before backpressure forces
	// the reader goroutine to wait. Generous enough that a normal TUI render
	// burst doesn't stall, small enough that a runaway agent can't OOM us.
	outputBuffer = 256

	// readChunkSize matches a typical TTY line buffer.
	readChunkSize = 4096

	// killGracePeriod is how long Kill waits for SIGINT to take effect before
	// escalating to SIGKILL + ptmx.Close.
	killGracePeriod = 2 * time.Second
)

// Binary is the executable name we shell out to. Override in tests.
var Binary = "claude"

// Adapter implements agent.Agent for Claude Code.
type Adapter struct {
	binary string

	mu      sync.Mutex
	cmd     *exec.Cmd
	ptmx    *os.File
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

	cmd := exec.CommandContext(ctx, a.binary, buildArgs(opts)...)
	cmd.Dir = opts.Cwd
	cmd.Env = mergeEnv(opts.Env)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("claudecode: pty.Start %s: %w", a.binary, err)
	}

	a.cmd = cmd
	a.ptmx = ptmx
	a.spawned = true

	go a.readLoop(cmd, ptmx)
	return nil
}

// Output returns the channel of agent events. Closes when the process exits.
func (a *Adapter) Output() <-chan agent.Event { return a.events }

// Resize informs the child process of a new terminal size via TIOCSWINSZ.
// Adapters must be spawned before Resize is meaningful; calls before Spawn
// return an error so callers don't silently lose their initial size.
func (a *Adapter) Resize(cols, rows int) error {
	a.mu.Lock()
	ptmx := a.ptmx
	a.mu.Unlock()
	if ptmx == nil {
		return fmt.Errorf("claudecode: not spawned")
	}
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("claudecode: invalid size %dx%d", cols, rows)
	}
	return pty.Setsize(ptmx, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

// Send writes input verbatim to the agent's PTY master. No transformations
// — callers are responsible for line endings and any escape encoding. This
// keeps Send symmetric with raw terminal input so attach mode can forward
// keystrokes as bytes.
func (a *Adapter) Send(input string) error {
	a.mu.Lock()
	ptmx := a.ptmx
	a.mu.Unlock()
	if ptmx == nil {
		return fmt.Errorf("claudecode: not spawned")
	}
	_, err := io.WriteString(ptmx, input)
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
	cmd, ptmx := a.cmd, a.ptmx
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
	_ = cmd.Process.Kill()
	if ptmx != nil {
		_ = ptmx.Close()
	}
	<-a.done
	return nil
}

// readLoop streams the PTY into the events channel, then waits on the child
// to harvest its exit code, then closes both the events channel and the done
// signal. This goroutine owns cmd.Wait — Kill must not call it.
func (a *Adapter) readLoop(cmd *exec.Cmd, ptmx *os.File) {
	defer close(a.done)
	defer close(a.events)

	buf := make([]byte, readChunkSize)
	for {
		n, err := ptmx.Read(buf)
		if n > 0 {
			a.events <- agent.Event{Kind: agent.EventOutput, Text: string(buf[:n])}
		}
		if err != nil {
			break
		}
	}

	exitCode := -1
	switch err := cmd.Wait().(type) {
	case nil:
		exitCode = 0
	case *exec.ExitError:
		exitCode = err.ExitCode()
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
	args = append(args, opts.ExtraArgs...)
	if opts.Prompt != "" {
		args = append(args, opts.Prompt)
	}
	return args
}

// mergeEnv overlays opts.Env on top of os.Environ.
func mergeEnv(extra map[string]string) []string {
	if len(extra) == 0 {
		return os.Environ()
	}
	base := os.Environ()
	for k, v := range extra {
		base = append(base, k+"="+v)
	}
	return base
}
