// Package agent defines the contract every coding-agent backend must implement.
// Adapters (claude-code, codex, aider, ...) wrap the agent's existing CLI binary;
// Swarm does not re-implement agent loops.
package agent

import "context"

// SpawnOpts carries every parameter an adapter needs to launch its CLI. Fields
// are independent — adapters use what applies and ignore the rest. This struct
// is the stable surface that profile-based configurations and per-session flags
// flow through, so prefer extending it over adding new method parameters.
type SpawnOpts struct {
	// Cwd is the directory the agent process is launched in. For Swarm this
	// is the session's worktree path.
	Cwd string

	// Prompt is the initial user message. Empty means open an interactive
	// session with no first turn.
	Prompt string

	// Model overrides the agent's default model when supported (e.g.
	// claude-sonnet-4-6 for Claude Code's --model flag).
	Model string

	// SkipPermissions enables the adapter's bypass-confirmation mode (e.g.
	// claude --dangerously-skip-permissions). Off by default. CS-222.
	SkipPermissions bool

	// ExtraArgs are passed verbatim to the agent CLI after Swarm's own args.
	ExtraArgs []string

	// Env adds or overrides environment variables for the child process.
	Env map[string]string

	// DumpPath, if non-empty, makes the adapter mirror every byte it reads
	// from the agent's PTY to this file (raw, pre-parse). Used to capture
	// reproducers for emulator-divergence debugging — set via the
	// SWARM_DUMP_PTY env var, not normally part of user config.
	DumpPath string

	// SessionID identifies the session in any per-session config the
	// adapter writes (e.g. Claude hooks). Adapters that don't use it
	// ignore the field.
	SessionID string

	// HooksDir, if non-empty, tells the adapter where the parent swarm
	// process expects hook marker files to land. The adapter wires this
	// into per-session agent config (e.g. Claude's .claude/settings.local
	// .json) and into SWARM_HOOKS_DIR for the spawned process to inherit.
	HooksDir string

	// ResumeID, if non-empty, asks the agent to continue an existing
	// conversation. For Claude Code this maps to `claude --resume <id>`.
	// Captured by Workspace from prior session output.
	ResumeID string
}

type Agent interface {
	Spawn(ctx context.Context, opts SpawnOpts) error
	Output() <-chan Event
	Send(input string) error
	// Resize tells the agent's underlying terminal what dimensions to render
	// for. Sessions are rendered into a virtual terminal of the same size,
	// so this keeps the agent's layout matched to what the user actually
	// sees in the focused pane.
	Resize(cols, rows int) error
	Kill() error
}

type EventKind int

const (
	EventOutput EventKind = iota
	EventToolUse
	EventPermissionRequest
	EventDone
	EventError
)

type Event struct {
	Kind   EventKind
	Text   string
	Tokens *TokenUsage
	// Err is non-nil for EventError. ExitCode is set on EventDone (-1 if
	// the process was killed before reporting one).
	Err      error
	ExitCode int
}

type TokenUsage struct {
	Model    string
	Input    int
	Output   int
	CacheHit int
}
