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

// Accept brings a worktree's changes into the main repo's current branch,
// then destroys the worktree. Refuses if the main repo has uncommitted
// changes or the merge isn't fast-forward-able.
//
// Behavior covers both common cases:
//   - Worktree already has commits past the base ref → ff-merge them.
//   - Worktree has only uncommitted changes → commit them as a new tip
//     ("swarm: accept session <id>"), then ff-merge.
//
// AcceptSelective covers the file-level path: revert specific files to
// the base before falling through to this same logic.
func (g *GitManager) Accept(ctx context.Context, w *Worktree) error {
	if err := EnsureCleanTree(ctx, w.RepoRoot); err != nil {
		return fmt.Errorf("accept: %w", err)
	}
	if err := commitWorktreeIfDirty(ctx, w, "swarm: accept session "+w.ID); err != nil {
		return fmt.Errorf("accept: commit worktree: %w", err)
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

// AcceptSelective is Accept's file-level cousin. discardFiles are reverted
// in the worktree to their base-ref state before we commit-and-merge what's
// left. Used by the diff view when the user marks some files keep and
// others discard.
func (g *GitManager) AcceptSelective(ctx context.Context, w *Worktree, discardFiles []string) error {
	if err := EnsureCleanTree(ctx, w.RepoRoot); err != nil {
		return fmt.Errorf("accept: %w", err)
	}
	for _, f := range discardFiles {
		if err := revertFileToBase(ctx, w.Path, w.BaseRef, f); err != nil {
			return fmt.Errorf("accept: revert %s: %w", f, err)
		}
	}
	return g.Accept(ctx, w)
}

// commitWorktreeIfDirty stages and commits all changes in the worktree if
// any are present. No-op on a clean tree.
func commitWorktreeIfDirty(ctx context.Context, w *Worktree, msg string) error {
	out, err := exec.CommandContext(ctx, "git", "-C", w.Path, "status", "--porcelain").Output()
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(out))) == 0 {
		return nil
	}
	if out, err := exec.CommandContext(ctx, "git", "-C", w.Path, "add", "-A").CombinedOutput(); err != nil {
		return fmt.Errorf("git add: %w: %s", err, strings.TrimSpace(string(out)))
	}
	cmd := exec.CommandContext(ctx, "git", "-C", w.Path, "commit", "-m", msg)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=swarm",
		"GIT_AUTHOR_EMAIL=swarm@local",
		"GIT_COMMITTER_NAME=swarm",
		"GIT_COMMITTER_EMAIL=swarm@local",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// revertFileToBase resets one file in the worktree to its content at the
// base ref. If the file didn't exist in the base (i.e. it's an addition the
// agent made), removes the file from the worktree instead.
func revertFileToBase(ctx context.Context, wtPath, baseRef, file string) error {
	if baseRef == "" {
		baseRef = "HEAD"
	}
	if out, err := exec.CommandContext(ctx, "git", "-C", wtPath,
		"checkout", baseRef, "--", file).CombinedOutput(); err == nil {
		return nil
	} else {
		// Likely the file didn't exist in base — remove it.
		_ = out
	}
	return os.RemoveAll(filepath.Join(wtPath, file))
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
