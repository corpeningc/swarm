package claudecode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectDir(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	got := ProjectDir(filepath.Join(home, "src", "my.repo"))
	want := filepath.Join(home, ".claude", "projects")
	if !strings.HasPrefix(got, want) {
		t.Fatalf("ProjectDir() = %q, want prefix %q", got, want)
	}
	slug := filepath.Base(got)
	if strings.ContainsAny(slug, `\/:.`) {
		t.Errorf("slug %q still contains path punctuation", slug)
	}
	if !strings.HasSuffix(slug, "src-my-repo") {
		t.Errorf("slug = %q, want it to end in src-my-repo", slug)
	}
}

func TestTranscriptLabel(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  string
	}{
		{
			"first typed turn",
			[]string{
				`{"type":"mode","mode":"normal"}`,
				`{"type":"user","message":{"role":"user","content":"fix  the\nlogin bug"}}`,
				`{"type":"user","message":{"role":"user","content":"later turn"}}`,
			},
			"fix the login bug",
		},
		{
			"summary wins",
			[]string{
				`{"type":"summary","summary":"Release pipeline fixes"}`,
				`{"type":"user","message":{"role":"user","content":"hello"}}`,
			},
			"Release pipeline fixes",
		},
		{
			"skips meta and injected turns",
			[]string{
				`{"type":"user","isMeta":true,"message":{"role":"user","content":"caveat"}}`,
				`{"type":"user","message":{"role":"user","content":"<system-reminder>noise</system-reminder>"}}`,
				`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"real ask"}]}}`,
			},
			"real ask",
		},
		{"no user turn", []string{`{"type":"mode","mode":"normal"}`}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "t.jsonl")
			if err := os.WriteFile(path, []byte(strings.Join(tt.lines, "\n")+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := transcriptLabel(path); got != tt.want {
				t.Errorf("transcriptLabel() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTranscriptLabelTruncates(t *testing.T) {
	long := strings.Repeat("a", summaryMaxLen*2)
	path := filepath.Join(t.TempDir(), "t.jsonl")
	line := `{"type":"summary","summary":"` + long + `"}`
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	got := transcriptLabel(path)
	if len([]rune(got)) != summaryMaxLen+1 || !strings.HasSuffix(got, "…") {
		t.Errorf("transcriptLabel() = %q (len %d), want %d chars ending in an ellipsis",
			got, len([]rune(got)), summaryMaxLen+1)
	}
}

func TestConversationsMissingDir(t *testing.T) {
	if got := Conversations(filepath.Join(t.TempDir(), "nope")); got != nil {
		t.Errorf("Conversations() = %v, want nil for a repo with no history", got)
	}
}
