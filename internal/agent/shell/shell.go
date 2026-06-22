// Package shell is a PTY-backed agent.Agent that runs the user's interactive
// shell in a session's worktree. It powers the Shell tab, where the user does
// git operations (commit, push, gh pr create, rebase) against the worktree's
// swarm/<id> branch — replacing swarm's old opinionated Accept flow.
package shell

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aymanbagabas/go-pty"

	"github.com/calebcorpening/swarm/internal/agent"
	"github.com/calebcorpening/swarm/internal/agent/ptyutil"
)

const (
	outputBuffer    = 256
	readChunkSize   = 4096
	killGracePeriod = 2 * time.Second
)

// Adapter implements agent.Agent by spawning an interactive shell under a PTY.
type Adapter struct {
	mu      sync.Mutex
	cmd     *pty.Cmd
	pt      pty.Pty
	events  chan agent.Event
	done    chan struct{}
	killed  atomic.Bool
	spawned bool
}

func New() *Adapter {
	return &Adapter{
		events: make(chan agent.Event, outputBuffer),
		done:   make(chan struct{}),
	}
}

// shellCommand resolves the interactive shell to launch and its args.
// Honors $SHELL on Unix; prefers pwsh → powershell → cmd on Windows.
func shellCommand() (string, []string) {
	if runtime.GOOS == "windows" {
		for _, c := range []string{"pwsh", "powershell"} {
			if p, err := exec.LookPath(c); err == nil {
				return p, []string{"-NoLogo"}
			}
		}
		if p, err := exec.LookPath("cmd"); err == nil {
			return p, nil
		}
		return "cmd", nil
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh, []string{"-i"}
	}
	for _, c := range []string{"bash", "sh"} {
		if p, err := exec.LookPath(c); err == nil {
			return p, []string{"-i"}
		}
	}
	return "sh", []string{"-i"}
}

func (a *Adapter) Spawn(ctx context.Context, opts agent.SpawnOpts) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.spawned {
		return fmt.Errorf("shell: already spawned")
	}

	bin, args := shellCommand()
	pt, err := pty.New()
	if err != nil {
		return fmt.Errorf("shell: pty.New: %w", err)
	}
	cmd := pt.CommandContext(ctx, bin, args...)
	cmd.Dir = opts.Cwd
	cmd.Env = mergeEnv(opts.Env)
	if err := cmd.Start(); err != nil {
		_ = pt.Close()
		return fmt.Errorf("shell: start %s: %w", bin, err)
	}

	a.cmd = cmd
	a.pt = pt
	a.spawned = true
	go a.readLoop(cmd, pt)
	return nil
}

func (a *Adapter) Output() <-chan agent.Event { return a.events }

func (a *Adapter) Resize(cols, rows int) error {
	a.mu.Lock()
	pt := a.pt
	a.mu.Unlock()
	if pt == nil {
		return fmt.Errorf("shell: not spawned")
	}
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("shell: invalid size %dx%d", cols, rows)
	}
	return pt.Resize(cols, rows)
}

func (a *Adapter) Send(input string) error {
	a.mu.Lock()
	pt := a.pt
	a.mu.Unlock()
	if pt == nil {
		return fmt.Errorf("shell: not spawned")
	}
	_, err := io.WriteString(pt, input)
	return err
}

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
	ptyutil.KillProcessTree(cmd.Process)
	_ = cmd.Process.Kill()
	if pt != nil {
		_ = pt.Close()
	}
	<-a.done
	return nil
}

func (a *Adapter) readLoop(cmd *pty.Cmd, pt pty.Pty) {
	defer close(a.done)
	defer close(a.events)
	buf := make([]byte, readChunkSize)
	for {
		n, err := pt.Read(buf)
		if n > 0 {
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

// mergeEnv overlays opts.Env on os.Environ and pins TERM to xterm-256color so
// the shell's output matches what swarm's virtual terminal can emulate.
func mergeEnv(extra map[string]string) []string {
	base := os.Environ()
	if _, ok := extra["TERM"]; !ok {
		base = append(base, "TERM=xterm-256color")
	}
	for k, v := range extra {
		base = append(base, k+"="+v)
	}
	return base
}
