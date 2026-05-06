// Spike: prove we can spawn an arbitrary CLI under a PTY with bidirectional IO.
// Run: go run . [command] [args...]
// Default: bash
package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/term"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		args = []string{"bash"}
	}

	c := exec.Command(args[0], args[1:]...)
	ptmx, err := pty.Start(c)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pty.Start: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = ptmx.Close() }()

	if term.IsTerminal(int(os.Stdin.Fd())) {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGWINCH)
		go func() {
			for range ch {
				if err := pty.InheritSize(os.Stdin, ptmx); err != nil {
					fmt.Fprintf(os.Stderr, "InheritSize: %v\n", err)
				}
			}
		}()
		ch <- syscall.SIGWINCH
	}

	stdinFd := int(os.Stdin.Fd())
	if term.IsTerminal(stdinFd) {
		oldState, err := term.MakeRaw(stdinFd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "MakeRaw: %v\n", err)
			os.Exit(1)
		}
		defer func() { _ = term.Restore(stdinFd, oldState) }()
	}

	go func() { _, _ = io.Copy(ptmx, os.Stdin) }()
	_, _ = io.Copy(os.Stdout, ptmx)

	if err := c.Wait(); err != nil {
		fmt.Fprintf(os.Stderr, "\nchild exited: %v\n", err)
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		os.Exit(1)
	}
}
