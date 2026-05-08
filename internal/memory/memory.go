// Package memory persists project-level conventions that compound across
// swarm sessions in a repo. The file lives at <repo>/.swarm/memory.md
// (gitignored), is plain Markdown the user (or claude, by request)
// curates over time, and gets auto-prepended to every new session's first
// turn so the agent inherits the repo's habits — naming conventions, test
// patterns, architectural decisions — without the user re-explaining.
//
// Memory is intentionally NOT a session log. Per-task narratives ("we
// migrated X on date Y") would pile up as noise the next time you spawn
// a session in an unrelated area of the codebase. memory.md is for
// stable knowledge, edited by hand or by asking the agent to update it
// when a real pattern emerges.
package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// File returns the path to a repo's memory file.
func File(repoRoot string) string {
	return filepath.Join(repoRoot, ".swarm", "memory.md")
}

// Read returns the memory contents (trimmed) or "" if the file doesn't
// exist, is empty, or can't be read.
func Read(repoRoot string) string {
	if repoRoot == "" {
		return ""
	}
	data, err := os.ReadFile(File(repoRoot))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// Append writes additional content to the memory file. Creates the file
// (and parent dir) if needed. Adds a separator if the file already has
// content so entries don't run together.
func Append(repoRoot, entry string) error {
	if repoRoot == "" || strings.TrimSpace(entry) == "" {
		return nil
	}
	path := File(repoRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	info, _ := os.Stat(path)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	if info != nil && info.Size() > 0 {
		if _, err := f.WriteString("\n\n"); err != nil {
			return err
		}
	}
	_, err = f.WriteString(strings.TrimRight(entry, "\n"))
	return err
}

// PromptWithMemory wraps a user prompt with the repo's memory as context.
// Returns the prompt unchanged if there's no memory to inject. Format is
// chosen to look like part of the user's first turn so the agent treats
// it as context without confusion about who wrote it.
func PromptWithMemory(repoRoot, prompt string) string {
	mem := Read(repoRoot)
	if mem == "" {
		return prompt
	}
	return fmt.Sprintf(
		"<project-memory>\nThe following is shared knowledge accumulated by previous swarm sessions in this repository. Treat it as background context — apply where relevant, ignore where stale.\n\n%s\n</project-memory>\n\n%s",
		mem, prompt,
	)
}

