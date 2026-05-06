package worktree

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

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
	out, err := exec.CommandContext(ctx, "git", "-C", repoRoot,
		"worktree", "add", "--detach", path, baseRef).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git worktree add %s %s: %w: %s",
			path, baseRef, err, strings.TrimSpace(string(out)))
	}
	return &Worktree{ID: id, Path: path, BaseRef: baseRef, RepoRoot: repoRoot}, nil
}

func (g *GitManager) Destroy(ctx context.Context, w *Worktree) error {
	out, err := exec.CommandContext(ctx, "git", "-C", w.RepoRoot,
		"worktree", "remove", "--force", w.Path).CombinedOutput()
	if err != nil {
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
