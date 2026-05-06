package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrDirtyTree is returned by EnsureCleanTree when the repo has uncommitted
// changes. The caller may proceed if the user passes --force.
var ErrDirtyTree = errors.New("repository has uncommitted changes")

// FindRepoRoot resolves the enclosing git repository root from cwd.
func FindRepoRoot(ctx context.Context, cwd string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("not inside a git repository: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// EnsureCleanTree returns ErrDirtyTree if there are uncommitted changes.
func EnsureCleanTree(ctx context.Context, repoRoot string) error {
	out, err := exec.CommandContext(ctx, "git", "-C", repoRoot,
		"status", "--porcelain").Output()
	if err != nil {
		return fmt.Errorf("git status: %w", err)
	}
	if len(strings.TrimSpace(string(out))) > 0 {
		return ErrDirtyTree
	}
	return nil
}

// EnsureGitignore appends `.swarm/` to repoRoot/.gitignore if not already
// present. Idempotent.
func EnsureGitignore(repoRoot string) error {
	path := filepath.Join(repoRoot, ".gitignore")
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, line := range strings.Split(string(existing), "\n") {
		if strings.TrimSpace(line) == ".swarm/" || strings.TrimSpace(line) == ".swarm" {
			return nil
		}
	}
	prefix := ""
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		prefix = "\n"
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(prefix + ".swarm/\n")
	return err
}
