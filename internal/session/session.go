// Package session models the lifecycle of one agent session: a unique ID,
// a base ref, a worktree path, an agent backend, a prompt, and a status.
package session

import "time"

type Status int

const (
	StatusPending Status = iota
	StatusRunning
	StatusAwaitingInput
	StatusComplete
	StatusFailed
	StatusKilled
	// StatusInterrupted: session was running when swarm exited and got
	// restored from state.json. The agent process is gone but the
	// worktree may still hold uncommitted work.
	StatusInterrupted
)

func (s Status) String() string {
	switch s {
	case StatusPending:
		return "pending"
	case StatusRunning:
		return "running"
	case StatusAwaitingInput:
		return "awaiting-input"
	case StatusComplete:
		return "complete"
	case StatusFailed:
		return "failed"
	case StatusKilled:
		return "killed"
	case StatusInterrupted:
		return "interrupted"
	}
	return "unknown"
}

type Session struct {
	ID        string
	Name      string // optional user label; Label() falls back to ID when empty
	RepoRoot  string
	BaseRef   string
	Branch    string // git branch checked out in the worktree (verbatim session name)
	Worktree  string
	AgentName string
	Prompt    string
	Status    Status
	CreatedAt time.Time
	UpdatedAt time.Time
	// ClaudeSessionID is captured from the agent's output (the
	// "claude --resume <uuid>" line Claude prints) so we can reattach
	// to the same conversation when the user spawns a new session into
	// an existing worktree. Empty if not yet captured.
	ClaudeSessionID string
}

// Label is the identifier shown to the user — the user-supplied name when
// set, otherwise the auto-generated ID. Always non-empty.
func (s *Session) Label() string {
	if s.Name != "" {
		return s.Name
	}
	return s.ID
}
