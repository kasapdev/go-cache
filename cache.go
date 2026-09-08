// Package cache provides a generic, thread-safe LRU (least-recently-used)
// cache with optional per-entry time-to-live (TTL) expiration.
//
// The cache is backed by a doubly-linked list (container/list) plus a map
// from key to list element, giving O(1) average-case Get, Put, and Delete
// operations. When the cache is over capacity, the least-recently-used
// entry is evicted to make room for the new one.
package cache

import (
	"container/list"
	"sync"
	"time"
)

// Clock abstracts the retrieval of the current time so that tests can
// inject a fake/mock implementation instead of relying on the real wall
// clock (and, in particular, instead of using time.Sleep to observe TTL
// expiration).
type Clock interface {
	// Now returns the current time.
	Now() time.Time
}

// realClock is the default Clock implementation, backed by time.Now.
type realClock struct{}

// Now returns the current wall-clock time.
func (realClock) Now() time.Time {
	return time.Now()
}

// entry is the value stored in each element of the cache's internal
// linked list.
type entry[K comparable, V any] struct {
	key   K
	value V
	// expiresAt is the time at which this entry should be considered
	// expired. A zero time.Time means the entry never expires.
	expiresAt time.Time
}

// expired reports whether the entry has a TTL and that TTL has passed as
// of the given "now".
func (e *entry[K, V]) expired(now time.Time) bool {
	return !e.expiresAt.IsZero() && now.After(e.expiresAt)
}

// Cache is a generic, thread-safe LRU cache with optional per-entry TTL.
//
// A Cache must be created with New or NewWithClock; the zero value is not
// usable. All methods are safe for concurrent use by multiple goroutines,
// guarded by a single sync.Mutex — Get counts as a "write" operation in
// terms of locking because a cache hit mutates the LRU ordering, so a
// plain Mutex (rather than an RWMutex) is used deliberately.
type Cache[K comparable, V any] struct {
	mu       sync.Mutex
	capacity int
	clock    Clock

	// ll orders entries from most-recently-used (front) to
	// least-recently-used (back).
	ll *list.List
	// items maps a key to its element in ll for O(1) lookup.
	items map[K]*list.Element

	// hits and misses are cumulative counters since the cache was
	// created; they are never reset automatically.
	hits   int
	misses int
}

// New creates a new Cache with the given capacity, using the real system
// clock for TTL calculations. Capacity must be at least 1; values less
// than 1 are treated as 1.
func New[K comparable, V any](capacity int) *Cache[K, V] {
	return NewWithClock[K, V](capacity, realClock{})
}

// NewWithClock creates a new Cache with the given capacity and an
// explicit Clock implementation. This is primarily intended for tests
// that need to control the notion of "now" without sleeping, but it can
// also be used in production if an alternate time source is desired.
// Capacity must be at least 1; values less than 1 are treated as 1.
func NewWithClock[K comparable, V any](capacity int, clock Clock) *Cache[K, V] {
	if capacity < 1 {
		capacity = 1
	}
	if clock == nil {
		clock = realClock{}
	}
	return &Cache[K, V]{
		capacity: capacity,
		clock:    clock,
		ll:       list.New(),
		items:    make(map[K]*list.Element),
	}
}

// Get looks up key in the cache. If the key is present and has not
// expired, Get returns its value and true, and marks the entry as
// most-recently-used. If the key is absent, or present but expired, Get
// returns the zero value of V and false; an expired entry is evicted as
// part of the lookup.
func (c *Cache[K, V]) Get(key K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.items[key]
	if !ok {
		c.misses++
		var zero V
		return zero, false
	}

	ent := el.Value.(*entry[K, V])
	if ent.expired(c.clock.Now()) {
		c.removeElement(el)
		c.misses++
		var zero V
		return zero, false
	}

	c.ll.MoveToFront(el)
	c.hits++
	return ent.value, true
}

// Put inserts or updates key with value, with no expiration. It marks the
// entry as most-recently-used. If the cache is over capacity after the
// insertion, the least-recently-used entry is evicted.
func (c *Cache[K, V]) Put(key K, value V) {
	c.put(key, value, 0)
}

// PutWithTTL inserts or updates key with value, which expires after ttl
// elapses (as measured by the cache's Clock). A ttl of 0 or less means
// the entry never expires, identical to calling Put. It marks the entry
// as most-recently-used. If the cache is over capacity after the
// insertion, the least-recently-used entry is evicted.
func (c *Cache[K, V]) PutWithTTL(key K, value V, ttl time.Duration) {
	c.put(key, value, ttl)
}

func (c *Cache[K, V]) put(key K, value V, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var expiresAt time.Time
	if ttl > 0 {
		expiresAt = c.clock.Now().Add(ttl)
	}

	if el, ok := c.items[key]; ok {
		ent := el.Value.(*entry[K, V])
		ent.value = value
		ent.expiresAt = expiresAt
		c.ll.MoveToFront(el)
		return
	}

	ent := &entry[K, V]{key: key, value: value, expiresAt: expiresAt}
	el := c.ll.PushFront(ent)
	c.items[key] = el

	if c.ll.Len() > c.capacity {
		c.evictOldest()
	}
}

// evictOldest removes the least-recently-used entry (the back of the
// list). The caller must hold c.mu.
func (c *Cache[K, V]) evictOldest() {
	el := c.ll.Back()
	if el != nil {
		c.removeElement(el)
	}
}

// removeElement removes el from both the list and the items map. The
// caller must hold c.mu.
func (c *Cache[K, V]) removeElement(el *list.Element) {
	ent := el.Value.(*entry[K, V])
	c.ll.Remove(el)
	delete(c.items, ent.key)
}

// Delete removes key from the cache, if present. It is a no-op if the key
// is not present.
func (c *Cache[K, V]) Delete(key K) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.items[key]; ok {
		c.removeElement(el)
	}
}

// Len returns the number of entries currently stored in the cache.
//
// Len counts all stored entries, including ones that have an expired TTL
// but have not yet been purged by a Get call or overwritten by a Put
// call — the cache does not proactively scan for expiration on a
// background timer. This keeps Len an O(1) operation. Callers that need
// an exact count of *live* (non-expired) entries should Get each key of
// interest, since Get always enforces expiration.
func (c *Cache[K, V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.ll.Len()
}

// Stats returns the cumulative number of cache hits and misses recorded
// by Get calls since the cache was created. These counters are never
// reset automatically and are not affected by Put or Delete calls.
func (c *Cache[K, V]) Stats() (hits, misses int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.hits, c.misses
}
