//go:build windows

// Package ptyutil holds cross-platform helpers shared by PTY-backed agent
// adapters (claudecode, shell).
package ptyutil

import (
	"os"
	"os/exec"
	"strconv"
)

// KillProcessTree terminates p and all of its descendants. os.Process.Kill on
// Windows only ends the named process, leaving children (MCP servers, nested
// git, shell subprocesses) alive and holding file handles inside the worktree.
// taskkill /T walks the child tree; /F forces termination.
func KillProcessTree(p *os.Process) {
	if p == nil {
		return
	}
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(p.Pid)).Run()
}
