package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// setupHookTimeout bounds how long a fresh-worktree setup script may run.
// Generous because the common case is dependency installation (npm/go/pip).
const setupHookTimeout = 10 * time.Minute

// RunSetupHook runs the repo's per-worktree setup script in worktreePath if
// one exists, so freshly-created worktrees come pre-installed/configured
// before the agent starts (node_modules and other gitignored artifacts don't
// come across with `git worktree add`). Looks for .swarm/setup.ps1 on Windows
// and .swarm/setup.sh elsewhere. No-op (nil) when no script is present.
func RunSetupHook(repoRoot, worktreePath string) error {
	name, interp := setupScript(repoRoot)
	if name == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), setupHookTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, interp[0], append(interp[1:], name)...)
	cmd.Dir = worktreePath
	cmd.Env = append(os.Environ(),
		"SWARM_REPO="+repoRoot,
		"SWARM_WORKTREE="+worktreePath,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("setup hook: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// setupScript returns the absolute path to the repo's setup script and the
// interpreter argv to run it, or "" if no script exists / no interpreter is
// available.
func setupScript(repoRoot string) (string, []string) {
	dir := filepath.Join(repoRoot, ".swarm")
	if runtime.GOOS == "windows" {
		if p := filepath.Join(dir, "setup.ps1"); fileExists(p) {
			for _, sh := range []string{"pwsh", "powershell"} {
				if bin, err := exec.LookPath(sh); err == nil {
					return p, []string{bin, "-NoProfile", "-File"}
				}
			}
		}
	}
	if p := filepath.Join(dir, "setup.sh"); fileExists(p) {
		if bin, err := exec.LookPath("bash"); err == nil {
			return p, []string{bin}
		}
		if bin, err := exec.LookPath("sh"); err == nil {
			return p, []string{bin}
		}
	}
	return "", nil
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

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
