package tui

import (
	"regexp"
	"strings"
)

// DiffFile is one file's worth of `git diff` output, plus the user's
// keep / discard selection. Default is keep.
type DiffFile struct {
	Path    string
	Content string // raw ANSI-colored diff content (header + hunks)
	Keep    bool
}

// DiffSnapshot is the parsed result of `git diff` for one session, plus
// the user's selection state and currently-highlighted file.
type DiffSnapshot struct {
	Files   []*DiffFile
	Cursor  int
	ScrollY int // first visible line of the selected file's diff content
}

// SelectedFiles returns paths the user wants to keep.
func (s *DiffSnapshot) SelectedFiles() []string {
	out := make([]string, 0, len(s.Files))
	for _, f := range s.Files {
		if f.Keep {
			out = append(out, f.Path)
		}
	}
	return out
}

// DiscardedFiles returns paths the user wants to revert.
func (s *DiffSnapshot) DiscardedFiles() []string {
	out := make([]string, 0)
	for _, f := range s.Files {
		if !f.Keep {
			out = append(out, f.Path)
		}
	}
	return out
}

// fileHeader matches `diff --git a/<path> b/<path>` (potentially preceded
// by ANSI bold). Captures the b-side path which is the "current" name —
// correct for adds, modifies, and renames-to.
var fileHeader = regexp.MustCompile(`(?m)^(?:\x1b\[[0-9;]*m)?diff --git a/[^\s]+ b/([^\s\x1b]+)`)

// parseDiff splits the output of `git diff --color=always <ref>` into one
// DiffFile per file. All files default to Keep=true.
func parseDiff(out string) *DiffSnapshot {
	if strings.TrimSpace(out) == "" {
		return &DiffSnapshot{}
	}
	headers := fileHeader.FindAllStringSubmatchIndex(out, -1)
	if len(headers) == 0 {
		return &DiffSnapshot{}
	}
	files := make([]*DiffFile, 0, len(headers))
	for i, h := range headers {
		start := h[0]
		var end int
		if i+1 < len(headers) {
			end = headers[i+1][0]
		} else {
			end = len(out)
		}
		// The path capture group is at indices h[2]:h[3].
		path := out[h[2]:h[3]]
		content := out[start:end]
		files = append(files, &DiffFile{
			Path:    path,
			Content: content,
			Keep:    true,
		})
	}
	return &DiffSnapshot{Files: files}
}
