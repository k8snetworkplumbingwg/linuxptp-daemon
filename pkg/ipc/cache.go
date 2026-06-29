package ipc

import (
	"sync"
	"time"
)

type cacheKey struct {
	msgType string
	profile string
}

// Cache tracks the latest IPC message per (type, profile) and provides
// a channel for outbound messages. It deduplicates by suppressing
// messages whose values match the last-emitted state for the same key.
// When enabled is false, Send is a no-op.
type Cache struct {
	enabled bool
	mu      sync.Mutex
	store   map[cacheKey]Message
	outCh   chan Message
}

// NewCache creates a cache with a buffered outbound channel of the given size.
// When enabled is false the cache discards all messages.
func NewCache(bufSize int, enabled bool) *Cache {
	c := &Cache{
		enabled: enabled,
		store:   make(map[cacheKey]Message),
	}
	if enabled {
		c.outCh = make(chan Message, bufSize)
	}
	return c
}

// Send stores a message in the cache and writes it to the outbound channel.
// Returns false if the message is a duplicate (same Values and IFace as the
// last message for this type/profile key). The write to the outbound channel
// is non-blocking: if the channel is full, the message is stored but not queued.
func (c *Cache) Send(msg Message) bool {
	if !c.enabled {
		return false
	}
	if msg.Version == 0 {
		msg.Version = Version
	}
	if msg.Timestamp == "" {
		msg.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	key := cacheKey{msgType: msg.Type, profile: msg.Profile}
	if prev, ok := c.store[key]; ok && valuesEqual(prev, msg) {
		return false
	}
	c.store[key] = msg

	select {
	case c.outCh <- msg:
	default:
	}
	return true
}

// Snapshot returns a copy of all cached messages.
func (c *Cache) Snapshot() []Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	msgs := make([]Message, 0, len(c.store))
	for _, msg := range c.store {
		msgs = append(msgs, msg)
	}
	return msgs
}

// Clear removes all cached messages.
func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.store = make(map[cacheKey]Message)
}

// Out returns the read-only outbound message channel.
func (c *Cache) Out() <-chan Message {
	return c.outCh
}

func valuesEqual(a, b Message) bool {
	if a.IFace != b.IFace {
		return false
	}
	switch av := a.Values.(type) {
	case StateValue:
		bv, ok := b.Values.(StateValue)
		return ok && av == bv
	case GNSSStateValue:
		bv, ok := b.Values.(GNSSStateValue)
		return ok && av == bv
	case SyncStateValue:
		bv, ok := b.Values.(SyncStateValue)
		return ok && av == bv
	case SyncEStateValue:
		bv, ok := b.Values.(SyncEStateValue)
		return ok && av == bv
	case ClockClassValue:
		bv, ok := b.Values.(ClockClassValue)
		return ok && av == bv
	case SyncEClockQualityValue:
		bv, ok := b.Values.(SyncEClockQualityValue)
		return ok && av == bv
	default:
		return false
	}
}
