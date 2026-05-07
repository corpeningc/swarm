package session

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestRegistry_PersistAndRestore(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")

	r := NewRegistry(statePath)
	id1 := r.NextID()
	id2 := r.NextID()
	now := time.Now()
	r.Add(&Handle{Session: &Session{ID: id1, Name: "alpha", RepoRoot: "/r1", Status: StatusRunning, CreatedAt: now}})
	r.Add(&Handle{Session: &Session{ID: id2, RepoRoot: "/r2", Status: StatusComplete, CreatedAt: now.Add(time.Second)}})

	r2, restored, err := LoadOrNewRegistry(statePath)
	if err != nil {
		t.Fatalf("LoadOrNewRegistry: %v", err)
	}
	if r2.Len() != 2 {
		t.Errorf("restored Len = %d, want 2", r2.Len())
	}
	if len(restored) != 2 {
		t.Errorf("restored handles = %d, want 2", len(restored))
	}
	if next := r2.NextID(); next != "sess-003" {
		t.Errorf("counter not restored: NextID = %q, want sess-003", next)
	}
	h, _ := r2.Get(id1)
	if h.Session.Status != StatusInterrupted {
		t.Errorf("running session not promoted to interrupted: %v", h.Session.Status)
	}
	if h.Agent != nil {
		t.Errorf("restored handle Agent should be nil")
	}
	if h.Worktree == nil || h.Worktree.RepoRoot != "/r1" {
		t.Errorf("worktree not rebuilt: %+v", h.Worktree)
	}
	h2, _ := r2.Get(id2)
	if h2.Session.Status != StatusComplete {
		t.Errorf("complete session got mutated on restore: %v", h2.Session.Status)
	}
}

func TestRegistry_PersistAcrossRemove(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")

	r := NewRegistry(statePath)
	id := r.NextID()
	r.Add(&Handle{Session: &Session{ID: id, CreatedAt: time.Now()}})
	r.Remove(id)

	r2, _, _ := LoadOrNewRegistry(statePath)
	if r2.Len() != 0 {
		t.Errorf("removed session still on disk: Len = %d", r2.Len())
	}
}

func TestRegistry_NoPathMeansNoPersist(t *testing.T) {
	r := NewRegistry("")
	r.Add(&Handle{Session: &Session{ID: "x", CreatedAt: time.Now()}})
	if r.Len() != 1 {
		t.Errorf("Len = %d, want 1", r.Len())
	}
}

func TestRegistry_AddGetRemove(t *testing.T) {
	r := NewRegistry("")
	id := r.NextID()
	h := &Handle{Session: &Session{ID: id, Status: StatusPending, CreatedAt: time.Now()}}
	r.Add(h)

	got, ok := r.Get(id)
	if !ok || got != h {
		t.Fatalf("Get after Add: ok=%v got=%v", ok, got)
	}
	if r.Len() != 1 {
		t.Errorf("Len = %d, want 1", r.Len())
	}

	r.Remove(id)
	if _, ok := r.Get(id); ok {
		t.Error("Get after Remove returned ok")
	}
	if r.Len() != 0 {
		t.Errorf("Len after Remove = %d, want 0", r.Len())
	}
}

func TestRegistry_NextIDIsUnique(t *testing.T) {
	r := NewRegistry("")
	const n = 100
	seen := make(map[string]struct{}, n)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := r.NextID()
			mu.Lock()
			seen[id] = struct{}{}
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(seen) != n {
		t.Errorf("got %d unique IDs across %d concurrent calls", len(seen), n)
	}
}

func TestRegistry_ListIsSortedByCreation(t *testing.T) {
	r := NewRegistry("")
	t0 := time.Now()
	for i := 0; i < 5; i++ {
		id := r.NextID()
		r.Add(&Handle{Session: &Session{ID: id, CreatedAt: t0.Add(time.Duration(i) * time.Millisecond)}})
	}
	list := r.List()
	for i := 1; i < len(list); i++ {
		if list[i].Session.CreatedAt.Before(list[i-1].Session.CreatedAt) {
			t.Fatalf("List not sorted: %v before %v at %d", list[i].Session.CreatedAt, list[i-1].Session.CreatedAt, i)
		}
	}
}

func TestRegistry_SetStatus(t *testing.T) {
	r := NewRegistry("")
	id := r.NextID()
	r.Add(&Handle{Session: &Session{ID: id, Status: StatusPending, CreatedAt: time.Now()}})
	r.SetStatus(id, StatusRunning)
	h, _ := r.Get(id)
	if h.Session.Status != StatusRunning {
		t.Errorf("Status = %v, want running", h.Session.Status)
	}
	if h.Session.UpdatedAt.IsZero() {
		t.Errorf("UpdatedAt not touched")
	}
}

func TestRegistry_ConcurrentReadersWriters(t *testing.T) {
	r := NewRegistry("")
	for i := 0; i < 10; i++ {
		id := r.NextID()
		r.Add(&Handle{Session: &Session{ID: id, CreatedAt: time.Now()}})
	}
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			for {
				select {
				case <-done:
					return
				default:
					_ = r.List()
					_ = r.Len()
				}
			}
		}()
	}
	for i := 0; i < 100; i++ {
		id := r.NextID()
		r.Add(&Handle{Session: &Session{ID: id, CreatedAt: time.Now()}})
		r.SetStatus(id, StatusRunning)
		r.Remove(id)
	}
	close(done)
}
