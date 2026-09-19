package cache

import (
	"sync"
	"testing"
	"time"
)

// fakeClock is a controllable Clock implementation for tests, so that TTL
// behavior can be exercised deterministically without real time.Sleep
// waits.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{now: start}
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

// TestLRUEvictionOrder proves genuine LRU (not FIFO) eviction behavior.
//
// Sequence and expected internal order (front = most-recently-used,
// back = least-recently-used):
//
//  1. Put(A) -> [A]
//  2. Put(B) -> [B, A]
//  3. Put(C) -> [C, B, A]              (capacity 3, exactly full)
//  4. Get(A) -> refreshes A            -> [A, C, B]  (B is now LRU)
//  5. Put(D) -> exceeds capacity (4 > 3), evicts the back entry, which is
//     B -> [D, A, C]
//
// So after step 5: B must have been evicted, while A (refreshed in step
// 4), C, and D must all still be present. If eviction were naive FIFO
// (evict-oldest-inserted regardless of access), A would have been evicted
// instead of B, since A was inserted first. Asserting B is gone and A
// survives is therefore proof of real LRU semantics.
func TestLRUEvictionOrder(t *testing.T) {
	c := New[string, int](3)

	c.Put("A", 1)
	c.Put("B", 2)
	c.Put("C", 3)

	// Refresh A's recency; B becomes the least-recently-used entry.
	if v, ok := c.Get("A"); !ok || v != 1 {
		t.Fatalf("Get(A) = (%d, %v), want (1, true)", v, ok)
	}

	// This insertion should evict B, the current LRU entry.
	c.Put("D", 4)

	if _, ok := c.Get("B"); ok {
		t.Errorf("Get(B) after eviction = ok=true, want B to have been evicted")
	}
	if v, ok := c.Get("A"); !ok || v != 1 {
		t.Errorf("Get(A) after eviction = (%d, %v), want (1, true); A should have survived", v, ok)
	}
	if v, ok := c.Get("C"); !ok || v != 3 {
		t.Errorf("Get(C) after eviction = (%d, %v), want (3, true)", v, ok)
	}
	if v, ok := c.Get("D"); !ok || v != 4 {
		t.Errorf("Get(D) after eviction = (%d, %v), want (4, true)", v, ok)
	}

	if got := c.Len(); got != 3 {
		t.Errorf("Len() = %d, want 3", got)
	}
}

// TestTTLExpiry uses a fake clock to deterministically test TTL
// expiration without any real-time sleeping.
func TestTTLExpiry(t *testing.T) {
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := newFakeClock(t0)
	c := NewWithClock[string, string](10, clock)

	const ttl = 5 * time.Second
	c.PutWithTTL("short", "expires-soon", ttl)
	c.Put("long", "never-expires")

	// Before the TTL elapses, both entries should be present.
	if v, ok := c.Get("short"); !ok || v != "expires-soon" {
		t.Fatalf("Get(short) before expiry = (%q, %v), want (\"expires-soon\", true)", v, ok)
	}
	if v, ok := c.Get("long"); !ok || v != "never-expires" {
		t.Fatalf("Get(long) before expiry = (%q, %v), want (\"never-expires\", true)", v, ok)
	}

	// Advance the fake clock past the TTL of "short".
	clock.Advance(ttl + 1*time.Second)

	if v, ok := c.Get("short"); ok {
		t.Errorf("Get(short) after expiry = (%q, %v), want zero value and false", v, ok)
	}
	if v, ok := c.Get("long"); !ok || v != "never-expires" {
		t.Errorf("Get(long) after \"short\" expired = (%q, %v), want (\"never-expires\", true)", v, ok)
	}
}

// TestStats asserts exact cumulative hit/miss counts across a mix of
// operations.
func TestStats(t *testing.T) {
	c := New[string, int](10)

	c.Put("a", 1)

	c.Get("a") // hit
	c.Get("b") // miss
	c.Get("a") // hit
	c.Get("c") // miss
	c.Get("c") // miss

	hits, misses := c.Stats()
	if hits != 2 {
		t.Errorf("hits = %d, want 2", hits)
	}
	if misses != 3 {
		t.Errorf("misses = %d, want 3", misses)
	}
}

// TestDeleteAndLen exercises basic Delete and Len behavior.
func TestDeleteAndLen(t *testing.T) {
	c := New[string, int](5)

	if got := c.Len(); got != 0 {
		t.Fatalf("Len() on empty cache = %d, want 0", got)
	}

	c.Put("a", 1)
	c.Put("b", 2)
	c.Put("c", 3)

	if got := c.Len(); got != 3 {
		t.Fatalf("Len() = %d, want 3", got)
	}

	c.Delete("b")

	if got := c.Len(); got != 2 {
		t.Errorf("Len() after Delete(b) = %d, want 2", got)
	}
	if _, ok := c.Get("b"); ok {
		t.Errorf("Get(b) after Delete(b) = ok=true, want false")
	}

	// Deleting a nonexistent key must be a safe no-op.
	c.Delete("does-not-exist")
	if got := c.Len(); got != 2 {
		t.Errorf("Len() after deleting nonexistent key = %d, want 2", got)
	}
}

// TestPutUpdatesExistingKey verifies that Put on an existing key updates
// its value and recency without growing the cache.
func TestPutUpdatesExistingKey(t *testing.T) {
	c := New[string, int](2)

	c.Put("a", 1)
	c.Put("a", 2)

	if got := c.Len(); got != 1 {
		t.Fatalf("Len() = %d, want 1", got)
	}
	if v, ok := c.Get("a"); !ok || v != 2 {
		t.Fatalf("Get(a) = (%d, %v), want (2, true)", v, ok)
	}
}

// TestConcurrency is a race-detector smoke test: many goroutines hammer
// Get/Put/Delete on a shared cache concurrently. It makes no precise
// assertions about final state (that would be inherently racy given
// concurrent writers), just that nothing panics or races, plus a light
// sanity check that Len never exceeds capacity.
func TestConcurrency(t *testing.T) {
	const capacity = 100
	c := New[int, int](capacity)

	const goroutines = 50
	const opsPerGoroutine = 200

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				key := (g*opsPerGoroutine + i) % (capacity * 2)
				switch i % 3 {
				case 0:
					c.Put(key, key)
				case 1:
					c.Get(key)
				case 2:
					c.Delete(key)
				}
			}
		}(g)
	}

	wg.Wait()

	if got := c.Len(); got < 0 || got > capacity {
		t.Errorf("Len() after concurrent ops = %d, want in [0, %d]", got, capacity)
	}

	hits, misses := c.Stats()
	if hits < 0 || misses < 0 {
		t.Errorf("Stats() = (%d, %d), want non-negative", hits, misses)
	}
}

func TestPeek_DoesNotChangeRecencyOrStats(t *testing.T) {
	c := New[string, int](2)
	c.Put("a", 1)
	c.Put("b", 2) // order: b, a  (a is LRU)

	if v, ok := c.Peek("a"); !ok || v != 1 {
		t.Fatalf("Peek(a) = %d, %v; want 1, true", v, ok)
	}
	if _, ok := c.Peek("missing"); ok {
		t.Fatal("Peek(missing) should report absent")
	}
	if hits, misses := c.Stats(); hits != 0 || misses != 0 {
		t.Fatalf("Peek must not touch stats, got hits=%d misses=%d", hits, misses)
	}

	// If Peek had refreshed "a", inserting "c" would evict "b" instead.
	c.Put("c", 3)
	if _, ok := c.Get("a"); ok {
		t.Fatal("a should have been evicted: Peek must not promote it")
	}
	if _, ok := c.Get("b"); !ok {
		t.Fatal("b should still be present")
	}
}

func TestPeek_ExpiredEntryIsAbsentButNotEvicted(t *testing.T) {
	clock := newFakeClock(time.Unix(1000, 0))
	c := NewWithClock[string, int](2, clock)
	c.PutWithTTL("a", 1, time.Second)
	clock.Advance(2 * time.Second)

	if _, ok := c.Peek("a"); ok {
		t.Fatal("Peek should treat an expired entry as absent")
	}
	if c.Len() != 1 {
		t.Fatalf("Peek must not evict; Len = %d, want 1", c.Len())
	}
}

func TestKeys_OrderedMRUFirstAndSkipsExpired(t *testing.T) {
	clock := newFakeClock(time.Unix(1000, 0))
	c := NewWithClock[string, int](5, clock)
	c.Put("a", 1)
	c.PutWithTTL("b", 2, time.Second)
	c.Put("c", 3)
	c.Get("a") // order: a, c, b

	clock.Advance(2 * time.Second) // b expires
	got := c.Keys()
	want := []string{"a", "c"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Keys() = %v, want %v", got, want)
	}

	got[0] = "mutated"
	if again := c.Keys(); again[0] != "a" {
		t.Fatalf("Keys must return a copy; cache saw %v", again)
	}
}

func TestClear_EmptiesCacheButKeepsStatsAndCapacity(t *testing.T) {
	c := New[string, int](2)
	c.Put("a", 1)
	c.Get("a")
	c.Get("nope")

	c.Clear()
	if c.Len() != 0 {
		t.Fatalf("Len after Clear = %d, want 0", c.Len())
	}
	if _, ok := c.Peek("a"); ok {
		t.Fatal("a should be gone after Clear")
	}
	if hits, misses := c.Stats(); hits != 1 || misses != 1 {
		t.Fatalf("Clear must keep stats, got hits=%d misses=%d", hits, misses)
	}

	// Still fully usable, capacity unchanged.
	c.Put("x", 1)
	c.Put("y", 2)
	c.Put("z", 3)
	if c.Len() != 2 {
		t.Fatalf("capacity should still be 2, Len = %d", c.Len())
	}
}
