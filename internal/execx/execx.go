// Package execx wraps os/exec.CommandContext with one platform tweak: on
// Windows, children are spawned without a console window. The desktop app is
// a GUI-subsystem process, so plainly exec'ing a console tool (git, gh,
// taskkill) allocates a visible console — a terminal window that flashes on
// every diff refresh, spawn, or discard.
package execx

import (
	"context"
	"os/exec"
)

// Command is a drop-in for exec.CommandContext that hides the child's console
// window on Windows. Use it for every non-PTY subprocess; PTY spawns (ConPTY)
// manage their own console.
func Command(ctx context.Context, name string, arg ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, arg...)
	hideWindow(cmd)
	return cmd
}
