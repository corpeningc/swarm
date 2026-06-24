package worktree

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ErrWorktreePathExists is returned by Create when the target path already
// has something on disk — typically an orphan from a prior swarm run that
// crashed or was killed without `swarm prune`. errors.Is detects it.
var ErrWorktreePathExists = errors.New("worktree path already exists")

// GitManager implements Manager by shelling out to git and gh.
type GitManager struct{}

func NewGitManager() *GitManager { return &GitManager{} }

// SwarmWorktreesDir is the path under repoRoot where Swarm puts its worktrees.
func SwarmWorktreesDir(repoRoot string) string {
	return filepath.Join(repoRoot, ".swarm", "worktrees")
}

// resolvePath collapses symlinks so paths emitted by git can be compared to
// paths constructed in Go. Falls back to the original on error (e.g. path
// does not yet exist).
func resolvePath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// CurrentBranch returns the branch checked out in the worktree at path, or ""
// if it can't be determined (detached HEAD, or an orphan path git no longer
// tracks). Best-effort; callers treat "" as "unknown".
func CurrentBranch(ctx context.Context, path string) string {
	out, err := exec.CommandContext(ctx, "git", "-C", path,
		"rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	b := strings.TrimSpace(string(out))
	if b == "HEAD" { // detached
		return ""
	}
	return b
}

func (g *GitManager) Create(ctx context.Context, repoRoot, baseRef, id, branch string) (*Worktree, error) {
	repoRoot = resolvePath(repoRoot)
	path := filepath.Join(SwarmWorktreesDir(repoRoot), id)
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("%w at %s — run `swarm prune` to clean orphans",
			ErrWorktreePathExists, path)
	}
	if branch == "" {
		branch = id
	}
	// Create the worktree on a fresh branch so git operations in the Shell
	// tab work against a real, pushable branch rather than a detached HEAD.
	out, err := exec.CommandContext(ctx, "git", "-C", repoRoot,
		"worktree", "add", "-b", branch, path, baseRef).CombinedOutput()
	if err != nil && strings.Contains(string(out), "already exists") {
		// Branch lingered from a prior run — attach a worktree to it instead.
		out, err = exec.CommandContext(ctx, "git", "-C", repoRoot,
			"worktree", "add", path, branch).CombinedOutput()
	}
	if err != nil {
		return nil, fmt.Errorf("git worktree add %s %s: %w: %s",
			path, baseRef, err, strings.TrimSpace(string(out)))
	}
	return &Worktree{ID: id, Path: path, BaseRef: baseRef, Branch: branch, RepoRoot: repoRoot}, nil
}

func (g *GitManager) Destroy(ctx context.Context, w *Worktree) error {
	// Best-effort: delete the session branch so discarded sessions don't leave
	// branches behind. Runs first because `worktree remove` frees the branch
	// for deletion. When Branch isn't populated (e.g. prune constructs a bare
	// Worktree), read it from git while the worktree is still on disk.
	branch := w.Branch
	if branch == "" {
		branch = CurrentBranch(ctx, w.Path)
	}
	defer func() {
		if branch != "" {
			_ = exec.CommandContext(ctx, "git", "-C", w.RepoRoot, "branch", "-D", branch).Run()
		}
	}()
	out, err := exec.CommandContext(ctx, "git", "-C", w.RepoRoot,
		"worktree", "remove", "--force", w.Path).CombinedOutput()
	if err != nil {
		// Git refuses when the path is no longer a registered working tree —
		// e.g. an orphan from a crashed run, or (on Windows) a directory git
		// can't touch because a lingering child process still holds a handle.
		// Fall back to deleting the directory ourselves, retrying because
		// Windows releases file locks lazily after the process tree dies.
		if rmErr := removeAllWithRetry(w.Path); rmErr != nil {
			return fmt.Errorf("git worktree remove %s: %w: %s",
				w.Path, err, strings.TrimSpace(string(out)))
		}
	}
	// Always prune. `git worktree remove` cleans its own admin entry, but the
	// RemoveAll fallback leaves <repo>/.git/worktrees/<id> behind — and a
	// stale admin entry is exactly what makes a later create at this path fail
	// with "is not a working tree". Best-effort: a prune failure isn't fatal.
	_ = exec.CommandContext(ctx, "git", "-C", w.RepoRoot, "worktree", "prune").Run()
	return nil
}

// removeAllWithRetry deletes path, retrying a few times with a short backoff.
// On Windows a just-killed process tree can keep file handles open for a brief
// window, so the first attempt may hit a sharing violation that succeeds on a
// retry. Returns nil if the path is gone (including already-absent).
func removeAllWithRetry(path string) error {
	var err error
	for range 5 {
		if err = forceRemoveAll(path); err == nil {
			return nil
		}
		if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return err
}

// forceRemoveAll removes path, clearing the read-only bit on every entry
// first. Plain os.RemoveAll fails on Windows when files are read-only — which
// git's pack/object files inside a worktree's .git routinely are — so we
// chmod the whole tree writable before deleting. (This is the dominant cause
// of "cannot remove working tree" discard failures on Windows.)
func forceRemoveAll(path string) error {
	if err := os.RemoveAll(path); err == nil {
		return nil
	}
	_ = filepath.WalkDir(path, func(p string, _ fs.DirEntry, err error) error {
		if err == nil {
			_ = os.Chmod(p, 0o666)
		}
		return nil
	})
	return os.RemoveAll(path)
}

func (g *GitManager) List(ctx context.Context, repoRoot string) ([]*Worktree, error) {
	repoRoot = resolvePath(repoRoot)
	out, err := exec.CommandContext(ctx, "git", "-C", repoRoot,
		"worktree", "list", "--porcelain").Output()
	if err != nil {
		return nil, fmt.Errorf("git worktree list: %w", err)
	}
	return parsePorcelain(string(out), repoRoot), nil
}

func (g *GitManager) ResolvePR(ctx context.Context, repoRoot string, prNumber int) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", "pr", "view",
		fmt.Sprint(prNumber), "--json", "headRefOid", "-q", ".headRefOid")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("gh pr view %d: %w", prNumber, err)
	}
	sha := strings.TrimSpace(string(out))
	if sha == "" {
		return "", fmt.Errorf("gh pr view %d returned empty SHA", prNumber)
	}
	return sha, nil
}

// parsePorcelain reads `git worktree list --porcelain` output. Each worktree is
// a block of `key value` lines terminated by a blank line; the main worktree
// comes first.
func parsePorcelain(out, repoRoot string) []*Worktree {
	var result []*Worktree
	swarmDir := SwarmWorktreesDir(repoRoot)
	for _, block := range strings.Split(strings.TrimSpace(out), "\n\n") {
		w := &Worktree{RepoRoot: repoRoot}
		for _, line := range strings.Split(block, "\n") {
			key, val, _ := strings.Cut(line, " ")
			switch key {
			case "worktree":
				w.Path = val
			case "branch":
				w.Branch = strings.TrimPrefix(val, "refs/heads/")
				w.BaseRef = w.Branch
			case "HEAD":
				if w.BaseRef == "" {
					w.BaseRef = val
				}
			}
		}
		if w.Path == "" {
			continue
		}
		// Derive ID from path if this is a Swarm-managed worktree.
		if rel, err := filepath.Rel(swarmDir, w.Path); err == nil &&
			!strings.HasPrefix(rel, "..") && rel != "." {
			w.ID = rel
		}
		result = append(result, w)
	}
	return result
}
