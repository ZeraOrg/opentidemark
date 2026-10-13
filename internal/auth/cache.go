// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"container/list"
	"sync"
	"time"
)

// ttlLRU is a small LRU cache whose entries also expire.
type ttlLRU[V any] struct {
	mu      sync.Mutex
	clock   Clock
	max     int
	ll      *list.List
	entries map[string]*list.Element
}

type lruEntry[V any] struct {
	key     string
	value   V
	expires time.Time
}

func newTTLLRU[V any](clock Clock, maxEntries int) *ttlLRU[V] {
	return &ttlLRU[V]{clock: clock, max: maxEntries, ll: list.New(), entries: map[string]*list.Element{}}
}

func (c *ttlLRU[V]) get(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var zero V
	el, ok := c.entries[key]
	if !ok {
		return zero, false
	}
	e := el.Value.(*lruEntry[V])
	if !c.clock.Now().Before(e.expires) {
		c.ll.Remove(el)
		delete(c.entries, key)
		return zero, false
	}
	c.ll.MoveToFront(el)
	return e.value, true
}

func (c *ttlLRU[V]) put(key string, value V, expires time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		el.Value = &lruEntry[V]{key: key, value: value, expires: expires}
		c.ll.MoveToFront(el)
		return
	}
	c.entries[key] = c.ll.PushFront(&lruEntry[V]{key: key, value: value, expires: expires})
	for c.ll.Len() > c.max {
		last := c.ll.Back()
		c.ll.Remove(last)
		delete(c.entries, last.Value.(*lruEntry[V]).key)
	}
}

func (c *ttlLRU[V]) purge() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ll.Init()
	c.entries = map[string]*list.Element{}
}

func (c *ttlLRU[V]) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}
