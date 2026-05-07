package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRead_Missing(t *testing.T) {
	if got := Read(t.TempDir()); got != "" {
		t.Errorf("missing file should produce empty string; got %q", got)
	}
}

func TestAppend_CreatesAndAccumulates(t *testing.T) {
	dir := t.TempDir()

	if err := Append(dir, "first entry"); err != nil {
		t.Fatalf("first append: %v", err)
	}
	if err := Append(dir, "second entry"); err != nil {
		t.Fatalf("second append: %v", err)
	}

	got := Read(dir)
	if !strings.Contains(got, "first entry") {
		t.Errorf("missing first entry: %q", got)
	}
	if !strings.Contains(got, "second entry") {
		t.Errorf("missing second entry: %q", got)
	}
	if !strings.Contains(got, "first entry\n\nsecond entry") {
		t.Errorf("entries not separated by blank line: %q", got)
	}
}

func TestAppend_EmptyEntryNoOp(t *testing.T) {
	dir := t.TempDir()
	if err := Append(dir, "  \n  "); err != nil {
		t.Errorf("blank entry should be no-op: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".swarm", "memory.md")); err == nil {
		t.Errorf("blank entry should not have created the file")
	}
}

func TestPromptWithMemory_NoMemoryReturnsPrompt(t *testing.T) {
	dir := t.TempDir()
	got := PromptWithMemory(dir, "fix the test")
	if got != "fix the test" {
		t.Errorf("no memory should return prompt unchanged; got %q", got)
	}
}

func TestPromptWithMemory_WrapsExisting(t *testing.T) {
	dir := t.TempDir()
	_ = Append(dir, "Use snake_case in DB columns.")
	got := PromptWithMemory(dir, "add a users table")
	if !strings.Contains(got, "<project-memory>") {
		t.Errorf("expected <project-memory> tag; got %q", got)
	}
	if !strings.Contains(got, "snake_case") {
		t.Errorf("memory contents missing; got %q", got)
	}
	if !strings.Contains(got, "add a users table") {
		t.Errorf("user prompt missing; got %q", got)
	}
}

func TestAcceptedEntry_Format(t *testing.T) {
	got := AcceptedEntry("auth-fix", "migrate JWT to opaque tokens")
	if !strings.HasPrefix(got, "## auth-fix") {
		t.Errorf("missing label header; got %q", got)
	}
	if !strings.Contains(got, "migrate JWT") {
		t.Errorf("missing prompt; got %q", got)
	}
}

