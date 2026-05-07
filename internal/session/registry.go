package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/calebcorpening/swarm/internal/agent"
	"github.com/calebcorpening/swarm/internal/worktree"
)

// Handle is the live runtime state of a session: its persistent metadata
// plus the agent process and worktree it owns. The Registry holds Handles;
// callers receive pointers and may read fields without locking, but should
// not mutate Status directly — use Registry.SetStatus.
//
// Restored sessions (read from state.json on startup) have Agent == nil;
// the live process is gone. UI code must check before calling Send / Kill.
type Handle struct {
	Session  *Session
	Worktree *worktree.Worktree
	Agent    agent.Agent
}

// Registry is a thread-safe store of active session handles. When statePath
// is non-empty, every mutation is mirrored to a JSON file on disk so
// sessions survive process restarts (CS-212 in the spec).
type Registry struct {
	mu        sync.RWMutex
	handles   map[string]*Handle
	counter   atomic.Uint64
	statePath string
}

// NewRegistry returns an empty registry. Pass a non-empty statePath to
// enable on-disk persistence; pass "" for an ephemeral registry (tests).
func NewRegistry(statePath string) *Registry {
	return &Registry{handles: make(map[string]*Handle), statePath: statePath}
}

// LoadOrNewRegistry reads existing state from statePath and returns both
// the registry (with all sessions restored as nil-Agent handles) and the
// list of restored handles for the caller to surface in the UI. If the
// file doesn't exist or is malformed, returns a fresh registry; the error
// is non-nil only on I/O failure beyond not-exists.
func LoadOrNewRegistry(statePath string) (*Registry, []*Handle, error) {
	r := NewRegistry(statePath)
	if statePath == "" {
		return r, nil, nil
	}
	data, err := os.ReadFile(statePath)
	if errors.Is(err, fs.ErrNotExist) {
		return r, nil, nil
	}
	if err != nil {
		return r, nil, err
	}
	var ps persistedState
	if err := json.Unmarshal(data, &ps); err != nil {
		// Don't kill the launch over corrupt state — start fresh,
		// surface the error so the caller can toast it.
		return r, nil, fmt.Errorf("state.json malformed: %w", err)
	}
	r.counter.Store(ps.Counter)
	restored := make([]*Handle, 0, len(ps.Sessions))
	for _, s := range ps.Sessions {
		// A running session in state.json means swarm exited without
		// shutting it down cleanly — promote it to interrupted so the UI
		// reflects reality.
		if s.Status == StatusRunning || s.Status == StatusPending {
			s.Status = StatusInterrupted
		}
		h := &Handle{
			Session: s,
			Worktree: &worktree.Worktree{
				ID:       s.ID,
				Path:     s.Worktree,
				BaseRef:  s.BaseRef,
				RepoRoot: s.RepoRoot,
			},
			// Agent stays nil — process is gone.
		}
		r.handles[s.ID] = h
		restored = append(restored, h)
	}
	return r, restored, nil
}

// persistedState is the on-disk shape of state.json.
type persistedState struct {
	Counter  uint64     `json:"counter"`
	Sessions []*Session `json:"sessions"`
}

// NextID generates a fresh session ID. Sortable by creation order.
func (r *Registry) NextID() string {
	n := r.counter.Add(1)
	return fmt.Sprintf("sess-%03d", n)
}

func (r *Registry) Add(h *Handle) {
	r.mu.Lock()
	r.handles[h.Session.ID] = h
	r.persistLocked()
	r.mu.Unlock()
}

func (r *Registry) Get(id string) (*Handle, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.handles[id]
	return h, ok
}

func (r *Registry) Remove(id string) {
	r.mu.Lock()
	delete(r.handles, id)
	r.persistLocked()
	r.mu.Unlock()
}

// List returns handles sorted by session creation time (oldest first), so the
// sidebar order is stable across renders.
func (r *Registry) List() []*Handle {
	r.mu.RLock()
	out := make([]*Handle, 0, len(r.handles))
	for _, h := range r.handles {
		out = append(out, h)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		return out[i].Session.CreatedAt.Before(out[j].Session.CreatedAt)
	})
	return out
}

// SetStatus updates a session's status atomically; safe to call from any
// goroutine. Touches UpdatedAt and persists.
func (r *Registry) SetStatus(id string, s Status) {
	r.mu.Lock()
	if h, ok := r.handles[id]; ok {
		h.Session.Status = s
		h.Session.UpdatedAt = time.Now()
		r.persistLocked()
	}
	r.mu.Unlock()
}

// Len reports current session count.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.handles)
}

// persistLocked writes the registry's current state to disk. Caller must
// hold r.mu (write lock).
func (r *Registry) persistLocked() {
	if r.statePath == "" {
		return
	}
	sessions := make([]*Session, 0, len(r.handles))
	for _, h := range r.handles {
		sessions = append(sessions, h.Session)
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].CreatedAt.Before(sessions[j].CreatedAt)
	})
	state := persistedState{Counter: r.counter.Load(), Sessions: sessions}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(r.statePath), 0755); err != nil {
		return
	}
	// Best-effort: write atomically via a temp file + rename so a crash
	// mid-write doesn't leave a truncated state.json.
	tmp := r.statePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return
	}
	_ = os.Rename(tmp, r.statePath)
}
