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
	}
	return "unknown"
}

type Session struct {
	ID         string
	RepoRoot   string
	BaseRef    string
	Worktree   string
	AgentName  string
	Prompt     string
	Status     Status
	CreatedAt  time.Time
	UpdatedAt  time.Time
}
