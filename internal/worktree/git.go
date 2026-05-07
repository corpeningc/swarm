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

func (g *GitManager) Create(ctx context.Context, repoRoot, baseRef, id string) (*Worktree, error) {
	repoRoot = resolvePath(repoRoot)
	path := filepath.Join(SwarmWorktreesDir(repoRoot), id)
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("%w at %s — run `swarm prune` to clean orphans",
			ErrWorktreePathExists, path)
	}
	out, err := exec.CommandContext(ctx, "git", "-C", repoRoot,
		"worktree", "add", "--detach", path, baseRef).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git worktree add %s %s: %w: %s",
			path, baseRef, err, strings.TrimSpace(string(out)))
	}
	return &Worktree{ID: id, Path: path, BaseRef: baseRef, RepoRoot: repoRoot}, nil
}

// Accept fast-forward-merges the worktree's HEAD into the main repository's
// current branch, then destroys the worktree. Refuses if the main repo has
// uncommitted changes, or if the merge isn't fast-forward-able. Both happen
// in normal use: switching branches in the main repo, or commits landing on
// the base branch since the worktree was created.
//
// Intentionally conservative: a non-ff merge could create conflicts the TUI
// can't resolve. Users with that case can `cd` into the worktree, resolve
// manually, then re-run accept.
func (g *GitManager) Accept(ctx context.Context, w *Worktree) error {
	if err := EnsureCleanTree(ctx, w.RepoRoot); err != nil {
		return fmt.Errorf("accept: %w", err)
	}
	headSha, err := revParse(ctx, w.Path, "HEAD")
	if err != nil {
		return fmt.Errorf("accept: read worktree HEAD: %w", err)
	}
	repoSha, err := revParse(ctx, w.RepoRoot, "HEAD")
	if err != nil {
		return fmt.Errorf("accept: read repo HEAD: %w", err)
	}
	if headSha == repoSha {
		// Worktree never advanced past base — nothing to merge.
		return g.Destroy(ctx, w)
	}
	out, err := exec.CommandContext(ctx, "git", "-C", w.RepoRoot,
		"merge", "--ff-only", headSha).CombinedOutput()
	if err != nil {
		return fmt.Errorf("accept: git merge --ff-only %s: %w: %s",
			headSha[:8], err, strings.TrimSpace(string(out)))
	}
	return g.Destroy(ctx, w)
}

func revParse(ctx context.Context, dir, ref string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", ref).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (g *GitManager) Destroy(ctx context.Context, w *Worktree) error {
	out, err := exec.CommandContext(ctx, "git", "-C", w.RepoRoot,
		"worktree", "remove", "--force", w.Path).CombinedOutput()
	if err != nil {
		// Git may refuse if the worktree was never registered (orphaned
		// directory). Fall back to removing the directory directly so
		// `swarm prune` can clean up after a crashed run.
		if rmErr := os.RemoveAll(w.Path); rmErr == nil {
			return nil
		}
		return fmt.Errorf("git worktree remove %s: %w: %s",
			w.Path, err, strings.TrimSpace(string(out)))
	}
	return nil
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
				w.BaseRef = strings.TrimPrefix(val, "refs/heads/")
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
