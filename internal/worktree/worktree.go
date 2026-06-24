// Package worktree wraps `git worktree` operations: create, destroy, list,
// and PR-ref resolution via `gh pr view`.
package worktree

import "context"

type Worktree struct {
	ID       string
	Path     string
	BaseRef  string // the commit-ish the worktree branched from (diff base)
	Branch   string // the branch checked out in the worktree (the session name, verbatim)
	RepoRoot string
}

type Manager interface {
	// Create adds a worktree at .swarm/worktrees/<id> on a new branch named
	// <branch>. id is the filesystem-safe slug (flat, no slashes); branch is
	// the verbatim session name and may contain slashes (e.g. "h/1234").
	Create(ctx context.Context, repoRoot, baseRef, id, branch string) (*Worktree, error)
	Destroy(ctx context.Context, w *Worktree) error
	List(ctx context.Context, repoRoot string) ([]*Worktree, error)
	ResolvePR(ctx context.Context, repoRoot string, prNumber int) (string, error)
}
