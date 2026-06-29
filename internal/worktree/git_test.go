package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePorcelain(t *testing.T) {
	repo := "/repo"
	out := strings.Join([]string{
		"worktree /repo",
		"HEAD abc123",
		"branch refs/heads/main",
		"",
		"worktree /repo/.swarm/worktrees/sess-001",
		"HEAD def456",
		"detached",
		"",
		"worktree /elsewhere/external",
		"HEAD 999aaa",
		"detached",
	}, "\n")

	got := parsePorcelain(out, repo)
	if len(got) != 3 {
		t.Fatalf("want 3 worktrees, got %d", len(got))
	}
	if got[0].Path != "/repo" || got[0].BaseRef != "main" || got[0].ID != "" {
		t.Errorf("main worktree wrong: %+v", got[0])
	}
	if got[1].ID != "sess-001" || got[1].BaseRef != "def456" {
		t.Errorf("swarm worktree wrong: %+v", got[1])
	}
	if got[2].ID != "" {
		t.Errorf("external worktree should have empty ID: %+v", got[2])
	}
}

func TestEnsureGitignore_AppendsAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()

	if err := EnsureGitignore(dir); err != nil {
		t.Fatalf("first call: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if !strings.Contains(string(got), ".swarm/") {
		t.Fatalf("expected .swarm/ in .gitignore, got %q", got)
	}

	if err := EnsureGitignore(dir); err != nil {
		t.Fatalf("second call: %v", err)
	}
	got2, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if string(got) != string(got2) {
		t.Errorf("not idempotent: %q -> %q", got, got2)
	}
}

func TestEnsureGitignore_PreservesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(path, []byte("node_modules/\ndist/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureGitignore(dir); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "node_modules/") {
		t.Errorf("clobbered existing entries: %q", got)
	}
	if !strings.Contains(string(got), ".swarm/") {
		t.Errorf("missing .swarm/: %q", got)
	}
}

// Full lifecycle test: init a temp repo, create a worktree, list it, destroy it.
// Skipped if git isn't on PATH.
func TestGitManager_Lifecycle(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	repo := t.TempDir()
	ctx := context.Background()

	mustRun(t, repo, "git", "init", "-b", "main")
	mustRun(t, repo, "git", "config", "user.email", "test@example.com")
	mustRun(t, repo, "git", "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, repo, "git", "add", "-A")
	mustRun(t, repo, "git", "commit", "-m", "init")

	g := NewGitManager()

	w, err := g.Create(ctx, repo, "main", "sess-test", "sess-test", "sess-test")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := os.Stat(w.Path); err != nil {
		t.Errorf("worktree path not created: %v", err)
	}

	list, err := g.List(ctx, repo)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var found bool
	for _, lw := range list {
		if lw.ID == "sess-test" {
			found = true
		}
	}
	if !found {
		t.Errorf("created worktree not in list: %+v", list)
	}

	if err := g.Destroy(ctx, w); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, err := os.Stat(w.Path); !os.IsNotExist(err) {
		t.Errorf("worktree path still exists after destroy: %v", err)
	}
}

func TestCreateNestedWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo := t.TempDir()
	ctx := context.Background()
	mustRun(t, repo, "git", "init", "-b", "main")
	mustRun(t, repo, "git", "config", "user.email", "test@example.com")
	mustRun(t, repo, "git", "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, repo, "git", "add", "-A")
	mustRun(t, repo, "git", "commit", "-m", "init")

	g := NewGitManager()
	// Flat id, nested relPath, slash-bearing branch.
	w, err := g.Create(ctx, repo, "main", "h-1234", "h/1234", "h/1234")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Verify the path nests as .../worktrees/h/1234 (compare the leaf and its
	// parent rather than the absolute path, which Create resolves through
	// symlinks/8.3 short names on Windows).
	if base, parent := filepath.Base(w.Path), filepath.Base(filepath.Dir(w.Path)); base != "1234" || parent != "h" {
		t.Errorf("Path = %q, want it to nest under .../h/1234", w.Path)
	}
	if w.Branch != "h/1234" {
		t.Errorf("Branch = %q, want h/1234", w.Branch)
	}
	if _, err := os.Stat(w.Path); err != nil {
		t.Errorf("nested worktree not created: %v", err)
	}

	if rels := SwarmWorktreeRelPaths(repo); len(rels) != 1 || rels[0] != "h/1234" {
		t.Errorf("SwarmWorktreeRelPaths = %v, want [h/1234]", rels)
	}

	if err := g.Destroy(ctx, w); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	// Leaf and its now-empty parent "h" should both be gone.
	if _, err := os.Stat(filepath.Join(SwarmWorktreesDir(repo), "h")); !os.IsNotExist(err) {
		t.Errorf("empty parent dir not cleaned up: %v", err)
	}
}

func TestEnsureCleanTree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo := t.TempDir()
	mustRun(t, repo, "git", "init", "-b", "main")
	mustRun(t, repo, "git", "config", "user.email", "t@t")
	mustRun(t, repo, "git", "config", "user.name", "t")
	os.WriteFile(filepath.Join(repo, "x"), []byte("x"), 0644)
	mustRun(t, repo, "git", "add", "-A")
	mustRun(t, repo, "git", "commit", "-m", "init")

	if err := EnsureCleanTree(context.Background(), repo); err != nil {
		t.Errorf("clean repo flagged dirty: %v", err)
	}

	os.WriteFile(filepath.Join(repo, "x"), []byte("y"), 0644)
	if err := EnsureCleanTree(context.Background(), repo); err != ErrDirtyTree {
		t.Errorf("dirty repo not flagged: %v", err)
	}
}

func mustRun(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v: %s", name, args, err, out)
	}
}
