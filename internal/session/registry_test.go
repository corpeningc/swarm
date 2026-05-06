package session

import (
	"sync"
	"testing"
	"time"
)

func TestRegistry_AddGetRemove(t *testing.T) {
	r := NewRegistry()
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
	r := NewRegistry()
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
	r := NewRegistry()
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
	r := NewRegistry()
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
	r := NewRegistry()
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
