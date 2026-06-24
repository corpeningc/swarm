//go:build !windows

// Package ptyutil holds cross-platform helpers shared by PTY-backed agent
// adapters (claudecode, shell).
package ptyutil

import (
	"os"
	"syscall"
)

// KillProcessTree sends SIGKILL to the process group led by p so children
// (MCP servers, nested git, shell subprocesses) don't outlive the agent and
// keep the worktree busy. go-pty starts the child in its own session, so the
// negative pid reaches every descendant; if it isn't a group leader the
// signal is a harmless no-op and the caller's Process.Kill still runs.
func KillProcessTree(p *os.Process) {
	if p == nil {
		return
	}
	_ = syscall.Kill(-p.Pid, syscall.SIGKILL)
}
