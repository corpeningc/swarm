package execx

import (
	"os/exec"
	"syscall"
)

// createNoWindow (CREATE_NO_WINDOW) stops a console child from allocating a
// console at all; HideWindow additionally hides any window it would show.
const createNoWindow = 0x08000000

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}
