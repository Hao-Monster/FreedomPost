// Package searchindex provides an in-memory cache for the search index.
// The cache is invalidated whenever posts are created, updated, or deleted.
// Format matches @freedompost/shared SearchIndexPayload exactly.
package searchindex

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// SearchIndexPayload matches @freedompost/shared SearchIndexPayload.
type SearchIndexPayload struct {
	Version   string           `json:"version"`
	Engine    string           `json:"engine"`
	Documents []SearchDocument `json:"documents"`
}

// SearchDocument matches @freedompost/shared SearchDocument.
type SearchDocument struct {
	ID        string    `json:"id"`
	Slug      string    `json:"slug"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Excerpt   string    `json:"excerpt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Cache holds the pre-serialized search index JSON.
// Thread-safe; invalidation triggers lazy rebuild on next request.
type Cache struct {
	mu      sync.RWMutex
	payload []byte // pre-serialized JSON, nil when invalid
}

// NewCache creates an empty search index cache.
func NewCache() *Cache {
	return &Cache{}
}

// Invalidate marks the cache as stale. The next call to GetOrBuild will
// rebuild from the provided documents.
func (c *Cache) Invalidate() {
	c.mu.Lock()
	c.payload = nil
	c.mu.Unlock()
}

// GetOrBuild returns the cached JSON bytes, building the index if stale.
// builder is called only when the cache is invalid; it should return the
// current search documents.
func (c *Cache) GetOrBuild(builder func() ([]SearchDocument, error)) ([]byte, error) {
	// Fast path: cache hit
	c.mu.RLock()
	if c.payload != nil {
		b := c.payload
		c.mu.RUnlock()
		return b, nil
	}
	c.mu.RUnlock()

	// Slow path: rebuild
	c.mu.Lock()
	defer c.mu.Unlock()
	// Double-check after acquiring write lock
	if c.payload != nil {
		return c.payload, nil
	}

	docs, err := builder()
	if err != nil {
		return nil, fmt.Errorf("build search index: %w", err)
	}

	payload := SearchIndexPayload{
		Version:   fmt.Sprintf("%d", time.Now().UnixMilli()),
		Engine:    "local-weighted",
		Documents: docs,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal search index: %w", err)
	}

	c.payload = data
	return data, nil
}
