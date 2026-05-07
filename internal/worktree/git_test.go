package worktree

import (
	"context"
	"errors"
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

	w, err := g.Create(ctx, repo, "main", "sess-test")
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

// TestGitManager_AcceptFastForward exercises the happy path: worktree adds
// a commit, Accept fast-forward-merges it into the main repo's current
// branch and destroys the worktree.
func TestGitManager_AcceptFastForward(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo := t.TempDir()
	ctx := context.Background()

	mustRun(t, repo, "git", "init", "-b", "main")
	mustRun(t, repo, "git", "config", "user.email", "t@t")
	mustRun(t, repo, "git", "config", "user.name", "t")
	os.WriteFile(filepath.Join(repo, "README"), []byte("hi"), 0644)
	mustRun(t, repo, "git", "add", "-A")
	mustRun(t, repo, "git", "commit", "-m", "init")
	if err := EnsureGitignore(repo); err != nil {
		t.Fatal(err)
	}
	mustRun(t, repo, "git", "add", "-A")
	mustRun(t, repo, "git", "commit", "-m", "ignore swarm")

	g := NewGitManager()
	w, err := g.Create(ctx, repo, "main", "sess-accept")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Add a commit inside the worktree.
	os.WriteFile(filepath.Join(w.Path, "feature.txt"), []byte("done"), 0644)
	mustRun(t, w.Path, "git", "add", "-A")
	mustRun(t, w.Path, "git", "commit", "-m", "feature")

	if err := g.Accept(ctx, w); err != nil {
		t.Fatalf("Accept: %v", err)
	}

	// Worktree should be gone; main repo should have the file.
	if _, err := os.Stat(w.Path); !os.IsNotExist(err) {
		t.Errorf("worktree still exists after accept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "feature.txt")); err != nil {
		t.Errorf("accepted commit didn't reach main repo: %v", err)
	}
}

// TestGitManager_AcceptRefusesDirty: main repo has uncommitted changes.
func TestGitManager_AcceptRefusesDirty(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo := t.TempDir()
	ctx := context.Background()

	mustRun(t, repo, "git", "init", "-b", "main")
	mustRun(t, repo, "git", "config", "user.email", "t@t")
	mustRun(t, repo, "git", "config", "user.name", "t")
	os.WriteFile(filepath.Join(repo, "README"), []byte("hi"), 0644)
	mustRun(t, repo, "git", "add", "-A")
	mustRun(t, repo, "git", "commit", "-m", "init")

	g := NewGitManager()
	w, err := g.Create(ctx, repo, "main", "sess-dirty")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer g.Destroy(ctx, w)

	// Dirty the main repo.
	os.WriteFile(filepath.Join(repo, "README"), []byte("changed"), 0644)

	if err := g.Accept(ctx, w); err == nil {
		t.Errorf("Accept on dirty repo should have errored")
	} else if !errors.Is(err, ErrDirtyTree) {
		t.Errorf("Accept error not ErrDirtyTree: %v", err)
	}
	if _, err := os.Stat(w.Path); err != nil {
		t.Errorf("worktree destroyed despite refused accept: %v", err)
	}
}

// TestGitManager_AcceptNothingToMerge: worktree never advanced past base.
// Should just destroy the worktree and return nil.
func TestGitManager_AcceptNothingToMerge(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo := t.TempDir()
	ctx := context.Background()

	mustRun(t, repo, "git", "init", "-b", "main")
	mustRun(t, repo, "git", "config", "user.email", "t@t")
	mustRun(t, repo, "git", "config", "user.name", "t")
	os.WriteFile(filepath.Join(repo, "README"), []byte("hi"), 0644)
	mustRun(t, repo, "git", "add", "-A")
	mustRun(t, repo, "git", "commit", "-m", "init")
	if err := EnsureGitignore(repo); err != nil {
		t.Fatal(err)
	}
	mustRun(t, repo, "git", "add", "-A")
	mustRun(t, repo, "git", "commit", "-m", "ignore swarm")

	g := NewGitManager()
	w, err := g.Create(ctx, repo, "main", "sess-noop")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := g.Accept(ctx, w); err != nil {
		t.Errorf("Accept on no-change worktree: %v", err)
	}
	if _, err := os.Stat(w.Path); !os.IsNotExist(err) {
		t.Errorf("worktree still exists after no-op accept: %v", err)
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
