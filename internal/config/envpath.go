package config

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// FixLaunchPath repairs $PATH when the process was started by macOS
// LaunchServices — i.e. by double-clicking a .app in Finder/Applications.
// GUI launches inherit a minimal PATH (/usr/bin:/bin:/usr/sbin:/sbin) instead
// of the user's interactive-shell PATH, so tools installed by nvm, Homebrew,
// etc. (claude, nvim, node) are invisible both to exec.LookPath in this
// process and to any shell it spawns. A terminal launch already has the rich
// PATH, so this is a no-op there (and on Windows).
//
// It asks the user's login shell for its PATH and adopts it when that is
// richer than what we have. Safe to call unconditionally at startup.
func FixLaunchPath() {
	if runtime.GOOS == "windows" {
		return
	}
	// A terminal-launched process already carries the user's PATH; only pay
	// the login-shell round-trip when PATH looks like the GUI minimal set.
	if !pathLooksMinimal(os.Getenv("PATH")) {
		return
	}

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh" // macOS default login shell
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// -l (login) sources ~/.zprofile — where nvm and macOS path_helper live;
	// -i (interactive) sources ~/.zshrc. Markers fence the value so noise
	// printed by rc files can't corrupt it.
	const pre, post = "__SWARM_PATH__", "__END__"
	out, err := exec.CommandContext(ctx, shell, "-lic",
		`printf '`+pre+`%s`+post+`' "$PATH"`).Output()
	if err != nil {
		return
	}
	s := string(out)
	i := strings.Index(s, pre)
	j := strings.Index(s, post)
	if i < 0 || j < i {
		return
	}
	got := s[i+len(pre) : j]
	if got != "" && len(got) > len(os.Getenv("PATH")) {
		_ = os.Setenv("PATH", got)
	}
}

// pathLooksMinimal reports whether path contains only the system directories a
// GUI launch gets by default — the signal that it was not started from a
// terminal and needs repair.
func pathLooksMinimal(path string) bool {
	for dir := range strings.SplitSeq(path, ":") {
		switch dir {
		case "/usr/bin", "/bin", "/usr/sbin", "/sbin", "":
			// system dir — expected in the minimal set
		default:
			return false // an enriched entry → PATH is already good
		}
	}
	return true
}
