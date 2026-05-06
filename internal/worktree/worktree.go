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
}
