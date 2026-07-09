package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Walks one repo through every remote state SeedRef distinguishes.
func TestSeedRef(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	ctx := context.Background()
	repo := t.TempDir()
	mustRun(t, repo, "git", "init", "-b", "main")
	mustRun(t, repo, "git", "config", "user.email", "t@t")
	mustRun(t, repo, "git", "config", "user.name", "t")
	commit := func(name string) {
		os.WriteFile(filepath.Join(repo, name), []byte(name), 0644)
		mustRun(t, repo, "git", "add", "-A")
		mustRun(t, repo, "git", "commit", "-m", name)
	}
	commit("a")

	if got := SeedRef(ctx, repo); got != "HEAD" {
		t.Errorf("no remote: got %q, want HEAD", got)
	}

	origin := t.TempDir()
	mustRun(t, origin, "git", "init", "--bare", "-b", "main")
	mustRun(t, repo, "git", "remote", "add", "origin", origin)
	mustRun(t, repo, "git", "push", "-u", "origin", "main")

	if got := SeedRef(ctx, repo); got != "HEAD" {
		t.Errorf("remote equal: got %q, want HEAD", got)
	}

	// Remote strictly ahead: push a commit, then rewind local.
	commit("b")
	mustRun(t, repo, "git", "push", "origin", "main")
	mustRun(t, repo, "git", "reset", "--hard", "HEAD~1")
	if got := SeedRef(ctx, repo); got != "origin/main" {
		t.Errorf("remote ahead: got %q, want origin/main", got)
	}

	// Diverged: local commit on top of the rewound state — local wins.
	commit("c")
	if got := SeedRef(ctx, repo); got != "HEAD" {
		t.Errorf("diverged: got %q, want HEAD", got)
	}

	// Local strictly ahead: sync up, then commit without pushing.
	mustRun(t, repo, "git", "push", "--force", "origin", "main")
	commit("d")
	if got := SeedRef(ctx, repo); got != "HEAD" {
		t.Errorf("local ahead: got %q, want HEAD", got)
	}
}
