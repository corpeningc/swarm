// Package genericcli is a PTY-backed agent.Agent for coding-agent CLIs that
// don't need swarm's Claude-specific wiring (hooks, --resume). It runs the
// binary interactively under a PTY in the worktree and builds its argv from a
// small per-agent Spec, so adding an agent is configuration rather than a new
// adapter. Codex and Aider presets ship here; Claude keeps its own adapter
// because of its hook/resume integration.
//
// Agents launched this way don't emit swarm hook markers, so their
// awaiting-input state comes from the silence heuristic rather than a Stop
// hook — see the workspace's evaluateAwaitingInput.
package genericcli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
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

// Spec describes how to launch one CLI agent.
type Spec struct {
	// Name is the agent's display name (also Session.AgentName).
	Name string
	// Binary is the executable resolved on PATH.
	Binary string
	// ModelFlag, if set, prefixes opts.Model (e.g. "--model").
	ModelFlag string
	// PromptAsArg passes opts.Prompt as a trailing positional argument when
	// true; when false the prompt is delivered interactively via Send after
	// spawn (some CLIs have no "initial message" flag).
	PromptAsArg bool
}

// Codex is the OpenAI Codex CLI preset.
func Codex() Spec {
	return Spec{Name: "codex", Binary: "codex", ModelFlag: "--model", PromptAsArg: true}
}

// Aider is the aider preset. aider has no clean interactive "first message"
// flag, so the prompt is sent after the session comes up.
func Aider() Spec {
	return Spec{Name: "aider", Binary: "aider", ModelFlag: "--model", PromptAsArg: false}
}

// Adapter implements agent.Agent for a Spec.
type Adapter struct {
	spec Spec

	mu          sync.Mutex
	cmd         *pty.Cmd
	pt          pty.Pty
	events      chan agent.Event
	done        chan struct{}
	killed      atomic.Bool
	spawned     bool
	deferredMsg string // prompt to Send once the PTY is up (PromptAsArg=false)
}

func New(spec Spec) *Adapter {
	return &Adapter{
		spec:   spec,
		events: make(chan agent.Event, outputBuffer),
		done:   make(chan struct{}),
	}
}

func (a *Adapter) Spawn(ctx context.Context, opts agent.SpawnOpts) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.spawned {
		return fmt.Errorf("%s: already spawned", a.spec.Name)
	}
	binPath, err := exec.LookPath(a.spec.Binary)
	if err != nil {
		return fmt.Errorf("%s: %s not found on PATH: %w", a.spec.Name, a.spec.Binary, err)
	}
	pt, err := pty.New()
	if err != nil {
		return fmt.Errorf("%s: pty.New: %w", a.spec.Name, err)
	}
	cmd := pt.CommandContext(ctx, binPath, a.buildArgs(opts)...)
	cmd.Dir = opts.Cwd
	cmd.Env = mergeEnv(opts.Env)
	if err := cmd.Start(); err != nil {
		_ = pt.Close()
		return fmt.Errorf("%s: start: %w", a.spec.Name, err)
	}
	if !a.spec.PromptAsArg {
		a.deferredMsg = opts.Prompt
	}
	a.cmd = cmd
	a.pt = pt
	a.spawned = true
	go a.readLoop(cmd, pt)
	return nil
}

func (a *Adapter) buildArgs(opts agent.SpawnOpts) []string {
	var args []string
	if a.spec.ModelFlag != "" && opts.Model != "" {
		args = append(args, a.spec.ModelFlag, opts.Model)
	}
	args = append(args, opts.ExtraArgs...)
	if a.spec.PromptAsArg && opts.Prompt != "" {
		args = append(args, opts.Prompt)
	}
	return args
}

func (a *Adapter) Output() <-chan agent.Event { return a.events }

func (a *Adapter) Resize(cols, rows int) error {
	a.mu.Lock()
	pt := a.pt
	a.mu.Unlock()
	if pt == nil {
		return fmt.Errorf("%s: not spawned", a.spec.Name)
	}
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("%s: invalid size %dx%d", a.spec.Name, cols, rows)
	}
	return pt.Resize(cols, rows)
}

func (a *Adapter) Send(input string) error {
	a.mu.Lock()
	pt := a.pt
	a.mu.Unlock()
	if pt == nil {
		return fmt.Errorf("%s: not spawned", a.spec.Name)
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
	// Deliver the deferred first message once the CLI has had a moment to
	// come up (PromptAsArg=false agents like aider).
	if a.deferredMsg != "" {
		go func(msg string) {
			time.Sleep(750 * time.Millisecond)
			_ = a.Send(msg + "\r")
		}(a.deferredMsg)
	}
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
