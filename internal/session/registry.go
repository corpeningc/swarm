package session

import (
	"fmt"
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
type Handle struct {
	Session  *Session
	Worktree *worktree.Worktree
	Agent    agent.Agent
}

// Registry is a thread-safe, in-memory store of active session handles.
// Persistence to state.json (CS-212) lives one layer up.
type Registry struct {
	mu       sync.RWMutex
	handles  map[string]*Handle
	counter  atomic.Uint64
}

func NewRegistry() *Registry {
	return &Registry{handles: make(map[string]*Handle)}
}

// NextID generates a fresh session ID. Sortable by creation order.
func (r *Registry) NextID() string {
	n := r.counter.Add(1)
	return fmt.Sprintf("sess-%03d", n)
}

func (r *Registry) Add(h *Handle) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handles[h.Session.ID] = h
}

func (r *Registry) Get(id string) (*Handle, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.handles[id]
	return h, ok
}

func (r *Registry) Remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.handles, id)
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
// goroutine (including the agent's reader). Touch UpdatedAt for free.
func (r *Registry) SetStatus(id string, s Status) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if h, ok := r.handles[id]; ok {
		h.Session.Status = s
		h.Session.UpdatedAt = time.Now()
	}
}

// Len reports current session count.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.handles)
}
