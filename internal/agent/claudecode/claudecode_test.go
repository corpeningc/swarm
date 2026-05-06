package claudecode

import (
	"context"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/calebcorpening/swarm/internal/agent"
)

func TestBuildArgs(t *testing.T) {
	tests := []struct {
		name string
		in   agent.SpawnOpts
		want []string
	}{
		{"empty", agent.SpawnOpts{}, nil},
		{
			"prompt only",
			agent.SpawnOpts{Prompt: "fix the test"},
			[]string{"fix the test"},
		},
		{
			"model + prompt",
			agent.SpawnOpts{Model: "claude-sonnet-4-6", Prompt: "hi"},
			[]string{"--model", "claude-sonnet-4-6", "hi"},
		},
		{
			"yolo + extras + prompt",
			agent.SpawnOpts{
				SkipPermissions: true,
				ExtraArgs:       []string{"--allowedTools", "Bash"},
				Prompt:          "go",
			},
			[]string{"--dangerously-skip-permissions", "--allowedTools", "Bash", "go"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := buildArgs(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("buildArgs() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestSpawn_OutputAndExit uses a deterministic shell command to verify the
// output channel streams text and EventDone reports the real exit code.
func TestSpawn_OutputAndExit(t *testing.T) {
	requireBinary(t, "bash")

	a := New("bash")
	if err := a.Spawn(context.Background(), agent.SpawnOpts{
		ExtraArgs: []string{"-c", "echo hi-from-child; exit 7"},
	}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	got, exit := drain(t, a, 3*time.Second)
	if !strings.Contains(got, "hi-from-child") {
		t.Errorf("missing expected output; got %q", got)
	}
	if exit != 7 {
		t.Errorf("exit code = %d, want 7", exit)
	}
}

// TestSend uses `cat` as a deterministic interactive process: anything we
// Send comes back through Output (via PTY echo or cat itself).
func TestSend(t *testing.T) {
	requireBinary(t, "cat")

	a := New("cat")
	if err := a.Spawn(context.Background(), agent.SpawnOpts{}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer a.Kill()

	if err := a.Send("ping-marker"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	deadline := time.After(3 * time.Second)
	var seen strings.Builder
	for !strings.Contains(seen.String(), "ping-marker") {
		select {
		case ev, ok := <-a.Output():
			if !ok {
				t.Fatalf("channel closed before marker; got %q", seen.String())
			}
			seen.WriteString(ev.Text)
		case <-deadline:
			t.Fatalf("timed out waiting for marker; got %q", seen.String())
		}
	}
}

func TestSpawn_TwiceErrors(t *testing.T) {
	requireBinary(t, "bash")
	a := New("bash")
	if err := a.Spawn(context.Background(), agent.SpawnOpts{
		ExtraArgs: []string{"-c", "sleep 5"},
	}); err != nil {
		t.Fatalf("first Spawn: %v", err)
	}
	defer a.Kill()
	if err := a.Spawn(context.Background(), agent.SpawnOpts{}); err == nil {
		t.Errorf("second Spawn should have errored")
	}
}

func TestKill_Idempotent(t *testing.T) {
	requireBinary(t, "bash")
	a := New("bash")
	if err := a.Spawn(context.Background(), agent.SpawnOpts{
		ExtraArgs: []string{"-c", "sleep 30"},
	}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if err := a.Kill(); err != nil {
		t.Fatalf("first Kill: %v", err)
	}
	// Second call must return promptly, not hang on done channel.
	doneCh := make(chan struct{})
	go func() { _ = a.Kill(); close(doneCh) }()
	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("second Kill hung")
	}
	if _, ok := <-a.Output(); ok {
		// Drain any remaining events. The channel must close.
		for range a.Output() {
		}
	}
}

func TestSend_BeforeSpawn(t *testing.T) {
	a := New("bash")
	if err := a.Send("nope"); err == nil {
		t.Errorf("Send before Spawn should error")
	}
}

// drain reads every event from a until the channel closes or timeout fires,
// returning the concatenated output text and the reported exit code.
func drain(t *testing.T, a *Adapter, timeout time.Duration) (string, int) {
	t.Helper()
	var sb strings.Builder
	exit := -999
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-a.Output():
			if !ok {
				return sb.String(), exit
			}
			switch ev.Kind {
			case agent.EventOutput:
				sb.WriteString(ev.Text)
			case agent.EventDone:
				exit = ev.ExitCode
			}
		case <-deadline:
			t.Fatalf("drain timed out after %v; got %q", timeout, sb.String())
		}
	}
}

func requireBinary(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s not on PATH", name)
	}
}
