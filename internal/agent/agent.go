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
