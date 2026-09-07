package claudecode

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Conversation is one Claude Code transcript found on disk: the id that
// `claude --resume` takes, plus enough context to recognise it in a picker.
type Conversation struct {
	ID        string    `json:"id"`
	Summary   string    `json:"summary"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// maxScanLines bounds how far into a transcript we read looking for a label.
// The opening user turn is within the first handful of lines; transcripts
// themselves run to megabytes.
const maxScanLines = 60

// summaryMaxLen keeps a picker entry to one line.
const summaryMaxLen = 90

// ProjectDir returns the directory Claude Code keeps cwd's transcripts in:
// ~/.claude/projects/<cwd with every non-alphanumeric character replaced by a
// dash>. Empty when the home directory can't be resolved.
func ProjectDir(cwd string) string {
	home, err := os.UserHomeDir()
	if err != nil || cwd == "" {
		return ""
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		abs = cwd
	}
	var b strings.Builder
	for _, r := range filepath.Clean(abs) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return filepath.Join(home, ".claude", "projects", b.String())
}

// Conversations lists the transcripts recorded for cwd, newest first. A
// conversation can only be resumed from the directory it was recorded in —
// Claude Code keys them by cwd — so callers pass the exact worktree path the
// new session will run in. Best-effort: an unreadable history yields nil.
func Conversations(cwd string) []Conversation {
	dir := ProjectDir(cwd)
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Conversation
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(dir, e.Name())
		out = append(out, Conversation{
			ID:        strings.TrimSuffix(e.Name(), ".jsonl"),
			Summary:   transcriptLabel(path),
			UpdatedAt: info.ModTime(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

// transcriptEntry is the sliver of a transcript line we care about. content is
// a string on a typed turn and an array of blocks once tools are involved, so
// it stays raw until we know which.
type transcriptEntry struct {
	Type    string `json:"type"`
	Summary string `json:"summary"`
	IsMeta  bool   `json:"isMeta"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// transcriptLabel picks a human label for a transcript: Claude's own summary
// when the file carries one, otherwise the first thing the user typed.
func transcriptLabel(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // transcript lines get long
	var firstUser string
	for i := 0; i < maxScanLines && sc.Scan(); i++ {
		var e transcriptEntry
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		if e.Type == "summary" && e.Summary != "" {
			return truncate(e.Summary)
		}
		if e.Type != "user" || e.IsMeta || firstUser != "" {
			continue
		}
		if text := userText(e.Message.Content); text != "" {
			firstUser = text
		}
	}
	return truncate(firstUser)
}

// userText pulls the plain text out of a user turn's content, which is either
// a bare string or a list of blocks. Tool results and the injected reminders
// that ride along with a turn are not what the user typed, so they're skipped.
func userText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return cleanText(s)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	for _, b := range blocks {
		if b.Type == "text" {
			if t := cleanText(b.Text); t != "" {
				return t
			}
		}
	}
	return ""
}

// cleanText flattens a turn to one line and drops the machinery Claude Code
// wraps around slash commands and system reminders.
func cleanText(s string) string {
	if strings.HasPrefix(strings.TrimSpace(s), "<") {
		return ""
	}
	return strings.Join(strings.Fields(s), " ")
}

func truncate(s string) string {
	if len(s) <= summaryMaxLen {
		return s
	}
	return strings.TrimSpace(s[:summaryMaxLen]) + "…"
}
