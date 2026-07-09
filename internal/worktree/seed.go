package worktree

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// SeedRef picks the ref a fresh worktree should branch from. When the main
// checkout's branch has a remote counterpart that is strictly ahead of local
// (after a best-effort fetch), new sessions seed from the remote so a stale
// local checkout doesn't start agents behind. Otherwise — no remote, offline,
// local ahead, or diverged — falls back to "HEAD".
func SeedRef(ctx context.Context, repoRoot string) string {
	branch := CurrentBranch(ctx, repoRoot)
	if branch == "" { // detached HEAD
		return "HEAD"
	}
	remoteRef := remoteRefFor(ctx, repoRoot, branch)
	if remoteRef == "" {
		return "HEAD"
	}
	// Refresh the remote-tracking ref so "ahead" reflects the remote now, not
	// the last fetch. Short timeout, best-effort: offline compares stale refs.
	remote, remoteBranch, _ := strings.Cut(remoteRef, "/")
	fetchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_ = exec.CommandContext(fetchCtx, "git", "-C", repoRoot,
		"fetch", "--quiet", remote, remoteBranch).Run()

	// Seed from the remote only when it's strictly ahead: local must be an
	// ancestor of the remote and the two must differ. Diverged histories keep
	// local (the user's commits win); equal means the seed is identical anyway.
	if exec.CommandContext(ctx, "git", "-C", repoRoot,
		"merge-base", "--is-ancestor", "HEAD", remoteRef).Run() != nil {
		return "HEAD"
	}
	if exec.CommandContext(ctx, "git", "-C", repoRoot,
		"merge-base", "--is-ancestor", remoteRef, "HEAD").Run() == nil {
		return "HEAD" // same commit
	}
	return remoteRef
}

// remoteRefFor returns the remote-tracking ref for branch: its configured
// upstream (e.g. "origin/main"), or "origin/<branch>" when that exists but no
// upstream is set. "" when the branch has no remote counterpart.
func remoteRefFor(ctx context.Context, repoRoot, branch string) string {
	out, err := exec.CommandContext(ctx, "git", "-C", repoRoot,
		"rev-parse", "--abbrev-ref", branch+"@{upstream}").Output()
	if err == nil {
		return strings.TrimSpace(string(out))
	}
	ref := "origin/" + branch
	if exec.CommandContext(ctx, "git", "-C", repoRoot,
		"rev-parse", "--verify", "--quiet", "refs/remotes/"+ref).Run() == nil {
		return ref
	}
	return ""
}
