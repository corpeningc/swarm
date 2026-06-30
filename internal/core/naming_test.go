package core

import "testing"

func TestWorktreeDirName(t *testing.T) {
	cases := map[string]string{
		"h/1234":        "h-1234",
		"feat/Login UX": "feat-login-ux",
		"  Foo  Bar  ":  "foo-bar",
		"a//b":          "a-b",
		"":              "",
		"!!!":           "",
		"WIP_thing-2":   "wip-thing-2",
	}
	for in, want := range cases {
		if got := worktreeDirName(in); got != want {
			t.Errorf("worktreeDirName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWorktreeRelPath(t *testing.T) {
	cases := map[string]string{
		"h/1234":        "h/1234",
		"feat/Login UX": "feat/login-ux",
		"a//b":          "a/b",
		"flat":          "flat",
		"":              "",
	}
	for in, want := range cases {
		if got := worktreeRelPath(in); got != want {
			t.Errorf("worktreeRelPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBranchNameFromLabel(t *testing.T) {
	cases := map[string]string{
		"h/1234":      "h/1234",
		"feat/login":  "feat/login",
		"Fix Bug 7":   "Fix-Bug-7",
		"/leading":    "leading",
		"trailing/":   "trailing",
		"a~b:c?d":     "abcd",   // illegal ref chars dropped
		"dots..here":  "dots.here", // ".." collapsed
		"":            "",
		"feat//login": "feat/login", // repeated slashes collapsed
	}
	for in, want := range cases {
		if got := branchNameFromLabel(in); got != want {
			t.Errorf("branchNameFromLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDirAndRelPathAgree guards the invariant that the flat ID and the nested
// path derive from the same segments — they must never drift, or a session's
// dir and ID would disagree and reattach would duplicate.
func TestDirAndRelPathAgree(t *testing.T) {
	for _, label := range []string{"h/1234", "feat/some thing", "a/b/c", "solo"} {
		dir := worktreeDirName(label)
		rel := worktreeRelPath(label)
		if want := replaceSlash(rel); dir != want {
			t.Errorf("label %q: dir %q disagrees with relPath %q (expected %q)", label, dir, rel, want)
		}
	}
}

func replaceSlash(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			out = append(out, '-')
		} else {
			out = append(out, s[i])
		}
	}
	return string(out)
}
