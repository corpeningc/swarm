package core

import (
	"context"
	"os/exec"
	"strings"
)

// sessionSlugSegments lowercases a label and splits it into sanitized path
// segments — one per slash-delimited component, each reduced to runs of
// [a-z0-9] joined by single dashes. Empty segments are dropped. It's the
// shared basis for both the flat session ID (segments joined by "-") and the
// nested worktree directory (segments as subdirs), so the two never drift.
func sessionSlugSegments(label string) []string {
	label = strings.TrimSpace(strings.ToLower(label))
	var segs []string
	for _, part := range strings.Split(label, "/") {
		var b strings.Builder
		prevDash := false
		for _, r := range part {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
				b.WriteRune(r)
				prevDash = false
			case r == '-' || r == '_' || r == ' ':
				if !prevDash && b.Len() > 0 {
					b.WriteByte('-')
					prevDash = true
				}
			}
		}
		if s := strings.TrimRight(b.String(), "-"); s != "" {
			segs = append(segs, s)
		}
	}
	return segs
}

// worktreeDirName is the flat, filesystem-safe session ID: slug segments joined
// by dashes, so "h/1234" yields "h-1234". Returns "" when nothing survives
// (caller generates an auto ID).
func worktreeDirName(label string) string {
	return strings.Join(sessionSlugSegments(label), "-")
}

// worktreeRelPath is the worktree's on-disk directory relative to the swarm
// worktrees root, nested to mirror the branch: "h/1234-foo" stays
// "h/1234-foo". Forward-slash separated. Returns "" when nothing survives.
func worktreeRelPath(label string) string {
	return strings.Join(sessionSlugSegments(label), "/")
}

// branchNameFromLabel turns a user-supplied label into a git branch name,
// preserving slashes so team conventions like "h/1234" or "feat/login" map to
// exactly that branch. Whitespace becomes dashes; git-ref-illegal characters
// are dropped; redundant or edge slashes/dashes are trimmed. Returns empty for
// an empty/all-illegal label (caller falls back to the dir ID).
func branchNameFromLabel(label string) string {
	label = strings.TrimSpace(label)
	var b strings.Builder
	prevSep := false // last written rune was a slash or dash
	for _, r := range label {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
			prevSep = r == '-'
		case r == '/':
			if !prevSep && b.Len() > 0 {
				b.WriteByte('/')
				prevSep = true
			}
		case r == ' ' || r == '\t':
			if !prevSep && b.Len() > 0 {
				b.WriteByte('-')
				prevSep = true
			}
		}
		// Everything else (git-ref-illegal: ~^:?*[\@ etc.) is dropped.
	}
	out := strings.Trim(b.String(), "-/.")
	out = strings.ReplaceAll(out, "..", ".") // git refs forbid ".."
	return out
}

// reconcileLegacyBranch renames a worktree's legacy swarm/<slug> branch to the
// clean, session-derived branch so the user's commits fast-forward the matching
// remote feature branch. No-op (returns false) unless the checked-out branch is
// swarm-prefixed, a distinct target is known, and that target doesn't already
// exist. Best-effort: a git failure leaves the branch as-is.
func reconcileLegacyBranch(ctx context.Context, path, current, want string) bool {
	if want == "" || want == current || !strings.HasPrefix(current, "swarm/") {
		return false
	}
	if exec.CommandContext(ctx, "git", "-C", path,
		"rev-parse", "--verify", "--quiet", "refs/heads/"+want).Run() == nil {
		return false
	}
	return exec.CommandContext(ctx, "git", "-C", path,
		"branch", "-m", current, want).Run() == nil
}
