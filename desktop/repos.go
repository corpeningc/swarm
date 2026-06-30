package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/corpeningc/swarm/internal/worktree"
)

// KnownRepos returns a de-duplicated, sorted list of git repositories worth
// offering in the new-session dropdown: the launch repo, every repo an existing
// session lives in, and any immediate sibling of the launch repo that is itself
// a git repo. The launch repo is always first so it stays the default.
func (a *App) KnownRepos() []string {
	seen := map[string]bool{}
	var rest []string
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		rest = append(rest, p)
	}

	for _, h := range a.orch.Registry().List() {
		add(h.Session.RepoRoot)
	}
	// Scan siblings of the launch repo (one level) for other repos — the common
	// case of several projects checked out side by side.
	def := a.orch.DefaultRepo()
	if def != "" {
		if entries, err := os.ReadDir(filepath.Dir(def)); err == nil {
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				p := filepath.Join(filepath.Dir(def), e.Name())
				if isGitRepo(p) {
					add(p)
				}
			}
		}
	}

	sort.Strings(rest)
	// Launch repo first, then the rest (with the launch repo removed if it also
	// turned up in the scan).
	out := make([]string, 0, len(rest)+1)
	if def != "" {
		out = append(out, def)
	}
	for _, p := range rest {
		if p != def {
			out = append(out, p)
		}
	}
	return out
}

// BrowseForRepo opens the native directory picker and returns the git root of
// the chosen directory. Errors if the selection isn't inside a repo; returns ""
// (no error) if the user cancels.
func (a *App) BrowseForRepo() (string, error) {
	dir, err := wruntime.OpenDirectoryDialog(a.ctx, wruntime.OpenDialogOptions{
		Title: "Select a git repository",
	})
	if err != nil {
		return "", err
	}
	if dir == "" {
		return "", nil // cancelled
	}
	root, err := worktree.FindRepoRoot(a.ctx, dir)
	if err != nil {
		return "", fmt.Errorf("%s is not inside a git repository", dir)
	}
	return root, nil
}

func isGitRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}
