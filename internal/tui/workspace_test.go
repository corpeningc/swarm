package tui

import "testing"

func TestExtractSessionID(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			"session_start payload",
			`{"session_id":"abc-123","cwd":"/repo","hook_event_name":"SessionStart"}`,
			"abc-123",
		},
		{
			"session_id later in object",
			`{"cwd":"/repo","hook_event_name":"SessionStart","session_id":"deadbeef-1234"}`,
			"deadbeef-1234",
		},
		{
			"whitespace around colon",
			`{ "session_id" :  "with-spaces"  }`,
			"with-spaces",
		},
		{
			"missing field",
			`{"cwd":"/repo"}`,
			"",
		},
		{"empty input", "", ""},
		{"malformed", `{"session_id": not a quote`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := extractSessionID([]byte(c.in)); got != c.want {
				t.Errorf("extractSessionID(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestWorktreeDirName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"   ", ""},
		{"auth-fix", "auth-fix"},
		{"Auth Fix", "auth-fix"},
		{"feature/login bug", "feature-login-bug"},
		{"!!!chaos@@@", "chaos"},
		{"-leading-and-trailing-", "leading-and-trailing"},
		{"multi   spaces", "multi-spaces"},
		{"PR-142 review", "pr-142-review"},
	}
	for _, c := range cases {
		if got := worktreeDirName(c.in); got != c.want {
			t.Errorf("worktreeDirName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBranchNameFromLabel(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"   ", ""},
		{"h/1234", "h/1234"},               // team convention preserved verbatim
		{"feat/login", "feat/login"},       // slashes kept
		{"PR-142", "PR-142"},               // case preserved (git refs are case-sensitive)
		{"fix login bug", "fix-login-bug"}, // spaces -> dashes
		{"//leading//double//", "leading/double"},
		{"trailing/", "trailing"},
		{"weird~^:?*name", "weirdname"}, // git-ref-illegal chars dropped
		{"a..b", "a.b"},                 // ".." is illegal in a git ref
	}
	for _, c := range cases {
		if got := branchNameFromLabel(c.in); got != c.want {
			t.Errorf("branchNameFromLabel(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
