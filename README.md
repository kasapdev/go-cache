# go-cache

A generic, thread-safe LRU (least-recently-used) cache for Go, with
optional per-entry TTL (time-to-live) expiration. Zero third-party
dependencies — built entirely on the Go standard library, using Go
generics (`container/list` for O(1)-ish LRU ordering) and
`sync.Mutex` for safe concurrent access.

## Installation

```bash
go get github.com/kasapdev/go-cache
```

## Usage

```go
package main

import (
	"fmt"
	"time"

	"github.com/kasapdev/go-cache"
)

func main() {
	// A cache of string keys to int values, holding at most 2 entries.
	c := cache.New[string, int](2)

	c.Put("a", 1)
	c.Put("b", 2)

	// A TTL variant: this entry expires after 50ms.
	c.PutWithTTL("temp", 99, 50*time.Millisecond)

	if v, ok := c.Get("a"); ok {
		fmt.Println("a =", v)
	}

	// Inserting a third key evicts the least-recently-used entry.
	c.Put("c", 3)

	if _, ok := c.Get("b"); !ok {
		fmt.Println("b was evicted")
	}

	c.Delete("c")

	hits, misses := c.Stats()
	fmt.Printf("hits=%d misses=%d len=%d\n", hits, misses, c.Len())
}
```

## API

### `func New[K comparable, V any](capacity int) *Cache[K, V]`

Creates a new cache with the given capacity, backed by the real system
clock. Capacity values less than 1 are treated as 1.

### `func NewWithClock[K comparable, V any](capacity int, clock Clock) *Cache[K, V]`

Creates a new cache with an explicit `Clock` implementation, primarily
useful for tests that need to control "now" without sleeping.

### `type Clock interface { Now() time.Time }`

Abstracts the current time. `New` uses a default implementation backed
by `time.Now`; supply your own (e.g. a fake clock) via `NewWithClock`.

### `func (c *Cache[K, V]) Get(key K) (V, bool)`

Returns the value for `key` and `true` if present and not expired.
On a hit, the entry becomes the most-recently-used entry. Returns the
zero value of `V` and `false` on a miss or if the entry has expired
(an expired entry is evicted as part of the lookup).

### `func (c *Cache[K, V]) Put(key K, value V)`

Inserts or updates `key` with `value`, with no expiration. Marks the
entry as most-recently-used. Evicts the least-recently-used entry if
the cache is over capacity afterward.

### `func (c *Cache[K, V]) PutWithTTL(key K, value V, ttl time.Duration)`

Same as `Put`, but the entry expires after `ttl` elapses (as measured
by the cache's `Clock`). A `ttl` of 0 or less means "never expires".

### `func (c *Cache[K, V]) Delete(key K)`

Removes `key` from the cache, if present. No-op otherwise.

### `func (c *Cache[K, V]) Len() int`

Returns the number of entries currently stored, including any whose
TTL has passed but hasn't yet been purged by a `Get` call. This keeps
`Len` an O(1) operation rather than a full scan; call `Get` on a
specific key if you need to know whether it is actually still live.

### `func (c *Cache[K, V]) Stats() (hits, misses int)`

Returns the cumulative number of `Get` hits and misses since the
cache was created. These counters are never reset automatically.

## Testing

```bash
go test ./...
```

To also run with the race detector (as CI does):

```bash
go test ./... -race -v
```

## License

MIT — see [LICENSE](LICENSE).
