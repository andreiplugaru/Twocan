package svc

import "sync"

// Locks is a map of mutexes, one per key, created on first use. The zero
// value is ready to use. Entries are never removed.
type Locks[K comparable] struct {
	mu sync.Mutex
	m  map[K]*sync.Mutex
}

// Lock locks k's mutex and returns the function that unlocks it.
func (l *Locks[K]) Lock(k K) (unlock func()) {
	l.mu.Lock()
	if l.m == nil {
		l.m = make(map[K]*sync.Mutex)
	}
	km, ok := l.m[k]
	if !ok {
		km = new(sync.Mutex)
		l.m[k] = km
	}
	l.mu.Unlock()
	km.Lock()
	return km.Unlock
}
