package preview

import (
	"container/list"
	"sync"
)

// Cache bounds. The budget counts payload content bytes (text content
// and image data URIs) plus a fixed per-entry overhead, so a full
// cache of maximum-size text previews stays well under the budget and
// tiny metadata cards cannot grow the entry count without bound.
const (
	cacheBudgetBytes   = 16 << 20 // 16 MiB
	cacheMaxEntries    = 64
	cacheEntryOverhead = 256 // fixed per-entry accounting overhead
)

// payloadCache is a bytes-bounded LRU of computed rich payloads keyed
// by path + mtime + size + provider kind. Thread-safe.
type payloadCache struct {
	mu    sync.Mutex
	ll    *list.List // front = most recently used
	items map[string]*list.Element
	bytes int
}

type cacheEntry struct {
	key  string
	p    Payload
	size int
}

func newPayloadCache() *payloadCache {
	return &payloadCache{ll: list.New(), items: map[string]*list.Element{}}
}

// payloadSize is the accounting size of one cached payload: the big
// variable parts (text content, image data URI) plus the fixed
// overhead.
func payloadSize(p Payload) int {
	n := cacheEntryOverhead
	if p.Text != nil {
		n += len(p.Text.Content)
	}
	if p.Image != nil {
		n += len(p.Image.DataURI)
	}
	return n
}

// get returns the cached payload for key and marks it most recently
// used.
func (c *payloadCache) get(key string) (Payload, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return Payload{}, false
	}
	c.ll.MoveToFront(el)
	return el.Value.(*cacheEntry).p, true
}

// put stores (replacing any previous value under key) and evicts
// least-recently-used entries until both bounds hold. A payload larger
// than the whole budget is not stored.
func (c *payloadCache) put(key string, p Payload) {
	size := payloadSize(p)
	if size > cacheBudgetBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		e := el.Value.(*cacheEntry)
		c.bytes += size - e.size
		e.p, e.size = p, size
		c.ll.MoveToFront(el)
	} else {
		el := c.ll.PushFront(&cacheEntry{key: key, p: p, size: size})
		c.items[key] = el
		c.bytes += size
	}
	for (c.bytes > cacheBudgetBytes || c.ll.Len() > cacheMaxEntries) && c.ll.Len() > 1 {
		c.evictOldest()
	}
}

// evictOldest drops the least-recently-used entry. Callers hold mu.
func (c *payloadCache) evictOldest() {
	el := c.ll.Back()
	if el == nil {
		return
	}
	e := el.Value.(*cacheEntry)
	c.ll.Remove(el)
	delete(c.items, e.key)
	c.bytes -= e.size
}

// len and byteSize expose the bounds for tests.
func (c *payloadCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}

func (c *payloadCache) byteSize() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bytes
}
