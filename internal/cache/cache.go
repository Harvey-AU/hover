package cache

import "sync"

// InMemoryCache is a concurrent-safe in-memory key-value store.
type InMemoryCache struct {
	mu    sync.RWMutex
	items map[string]any
}

func NewInMemoryCache() *InMemoryCache {
	return &InMemoryCache{items: make(map[string]any)}
}

func (c *InMemoryCache) Get(key string) (any, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	item, found := c.items[key]
	return item, found
}

func (c *InMemoryCache) Set(key string, value any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = value
}

func (c *InMemoryCache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, key)
}
