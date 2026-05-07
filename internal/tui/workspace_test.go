package tui

import "testing"

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
