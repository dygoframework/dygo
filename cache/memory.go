package cache

import (
	"container/list"
	"context"
	"sync"
	"time"
)

// Memory is an in-process [Store] that keeps at most a fixed number of
// entries and evicts the least recently used one when full. Expired entries
// are removed when read or evicted.
type Memory struct {
	mu    sync.Mutex
	max   int
	items map[string]*list.Element
	lru   *list.List // front is the most recently used
	now   func() time.Time
}

type entry struct {
	key     string
	val     []byte
	expires time.Time // zero means never
}

// NewMemory returns a memory store for up to size entries (at least 1).
func NewMemory(size int) *Memory {
	return &Memory{max: max(1, size), items: map[string]*list.Element{}, lru: list.New(), now: time.Now}
}

// Len returns the number of entries, including expired ones not yet removed.
func (m *Memory) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lru.Len()
}

// Get implements Store. The returned slice must not be modified.
func (m *Memory) Get(_ context.Context, key string) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	el, ok := m.items[key]
	if !ok {
		return nil, false, nil
	}
	e := el.Value.(*entry)
	if !e.expires.IsZero() && !m.now().Before(e.expires) {
		m.remove(el)
		return nil, false, nil
	}
	m.lru.MoveToFront(el)
	return e.val, true, nil
}

// Set implements Store. ttl 0 means no expiry.
func (m *Memory) Set(_ context.Context, key string, val []byte, ttl time.Duration) error {
	var exp time.Time
	if ttl > 0 {
		exp = m.now().Add(ttl)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if el, ok := m.items[key]; ok {
		e := el.Value.(*entry)
		e.val, e.expires = val, exp
		m.lru.MoveToFront(el)
		return nil
	}
	m.items[key] = m.lru.PushFront(&entry{key: key, val: val, expires: exp})
	for m.lru.Len() > m.max {
		m.remove(m.lru.Back())
	}
	return nil
}

// Delete implements Store.
func (m *Memory) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if el, ok := m.items[key]; ok {
		m.remove(el)
	}
	return nil
}

func (m *Memory) remove(el *list.Element) {
	m.lru.Remove(el)
	delete(m.items, el.Value.(*entry).key)
}
