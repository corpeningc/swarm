// Package agent defines the contract every coding-agent backend must implement.
// Adapters (claude-code, codex, aider, ...) wrap the agent's existing CLI binary;
// Swarm does not re-implement agent loops.
package agent

import "context"

type Agent interface {
	Spawn(ctx context.Context, prompt string, cwd string) error
	Output() <-chan Event
	Send(input string) error
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
}

type TokenUsage struct {
	Model    string
	Input    int
	Output   int
	CacheHit int
}
