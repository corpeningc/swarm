// Package worktree wraps `git worktree` operations: create, destroy, list,
// and PR-ref resolution via `gh pr view`.
package worktree

import "context"

type Worktree struct {
	ID       string
	Path     string
	BaseRef  string
	RepoRoot string
}

type Manager interface {
	Create(ctx context.Context, repoRoot, baseRef, id string) (*Worktree, error)
	Destroy(ctx context.Context, w *Worktree) error
	List(ctx context.Context, repoRoot string) ([]*Worktree, error)
	ResolvePR(ctx context.Context, repoRoot string, prNumber int) (string, error)
	// Accept fast-forward-merges the worktree's HEAD into the main repo's
	// current branch and then destroys the worktree. Returns an error
	// (without destroying the worktree) if the main repo has uncommitted
	// changes or the merge isn't fast-forward-able.
	Accept(ctx context.Context, w *Worktree) error
	// AcceptSelective reverts the listed files in the worktree to their
	// base-ref state, then runs Accept on what's left. Used by the diff
	// view's per-file keep/discard.
	AcceptSelective(ctx context.Context, w *Worktree, discardFiles []string) error
}
