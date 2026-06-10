package orchestration

import (
	"fmt"
	"sync"
	"testing"
)

func TestMemory_SetGet(t *testing.T) {
	m := NewSharedMemory()
	m.Set("k", "v", "task1")
	v, ok := m.Get("k")
	if !ok || v != "v" {
		t.Errorf("Get = %v, ok=%v", v, ok)
	}
	if !m.Has("k") {
		t.Error("Has should be true")
	}
	if _, ok := m.Get("missing"); ok {
		t.Error("missing key should not be present")
	}
}

func TestMemory_GetString(t *testing.T) {
	m := NewSharedMemory()
	m.Set("s", "hello", "a")
	m.Set("n", 42, "a")
	if m.GetString("s") != "hello" {
		t.Errorf("GetString(s) = %q", m.GetString("s"))
	}
	if m.GetString("n") != "" {
		t.Error("GetString on a non-string should return empty")
	}
	if m.GetString("missing") != "" {
		t.Error("GetString on a missing key should return empty")
	}
}

func TestMemory_OverwriteAndHistory(t *testing.T) {
	m := NewSharedMemory()
	m.Set("k", "v1", "task1")
	m.Set("k", "v2", "task2")
	if v, _ := m.Get("k"); v != "v2" {
		t.Errorf("current = %v, want v2", v)
	}
	hist := m.History("k")
	if len(hist) != 2 || hist[0].Value != "v1" || hist[1].Author != "task2" {
		t.Errorf("history = %+v", hist)
	}
}

func TestMemory_Delete(t *testing.T) {
	m := NewSharedMemory()
	m.Set("k", "v", "a")
	m.Delete("k")
	if m.Has("k") {
		t.Error("key should be gone after Delete")
	}
	// History is retained.
	if len(m.History("k")) != 1 {
		t.Error("history should survive Delete")
	}
}

func TestMemory_KeysAndSnapshot(t *testing.T) {
	m := NewSharedMemory()
	m.Set("a", 1, "x")
	m.Set("b", 2, "x")
	if len(m.Keys()) != 2 || m.Len() != 2 {
		t.Errorf("keys = %v, len = %d", m.Keys(), m.Len())
	}
	snap := m.Snapshot()
	if snap["a"].Value != 1 || snap["b"].Author != "x" {
		t.Errorf("snapshot = %+v", snap)
	}
	// Snapshot is a copy: mutating it doesn't affect the store.
	delete(snap, "a")
	if !m.Has("a") {
		t.Error("snapshot should be a copy")
	}
}

func TestMemory_ConcurrentSafe(t *testing.T) {
	m := NewSharedMemory()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				key := fmt.Sprintf("k%d", n)
				m.Set(key, j, fmt.Sprintf("g%d", n))
				_, _ = m.Get(key)
				_ = m.Keys()
				_ = m.Snapshot()
			}
		}(i)
	}
	wg.Wait()
	if m.Len() != 16 {
		t.Errorf("expected 16 keys, got %d", m.Len())
	}
}
