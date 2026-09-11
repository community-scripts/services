package main

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// CacheConfig holds cache configuration
type CacheConfig struct {
	RedisURL    string
	EnableRedis bool
	DefaultTTL  time.Duration
}

// Cache provides caching functionality with Redis or in-memory fallback
// Supports stale-while-revalidate: returns stale data immediately while refreshing in background
type Cache struct {
	redis      *redis.Client
	useRedis   bool
	defaultTTL time.Duration

	// In-memory cache
	mu      sync.RWMutex
	memData map[string]cacheEntry

	// Refresh tracking: prevents multiple concurrent refreshes for the same key
	refreshMu  sync.Mutex
	refreshing map[string]bool
}

type cacheEntry struct {
	data      []byte
	expiresAt time.Time // When the entry becomes stale (soft expiry)
	deadAt    time.Time // When the entry is truly expired and must be removed (hard expiry = 2x TTL)
}

// NewCache creates a new cache instance
func NewCache(cfg CacheConfig) *Cache {
	c := &Cache{
		defaultTTL: cfg.DefaultTTL,
		memData:    make(map[string]cacheEntry),
		refreshing: make(map[string]bool),
	}

	if cfg.EnableRedis && cfg.RedisURL != "" {
		opts, err := redis.ParseURL(cfg.RedisURL)
		if err != nil {
			log.Printf("WARN: invalid redis URL, using in-memory cache: %v", err)
			return c
		}

		client := redis.NewClient(opts)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		if err := client.Ping(ctx).Err(); err != nil {
			log.Printf("WARN: redis connection failed, using in-memory cache: %v", err)
			return c
		}

		c.redis = client
		c.useRedis = true
		log.Printf("INFO: connected to Redis for caching")
	}

	// Start cleanup goroutine for in-memory cache
	if !c.useRedis {
		go c.cleanupLoop()
	}

	return c
}

func (c *Cache) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		c.mu.Lock()
		now := time.Now()
		for k, v := range c.memData {
			// Only remove truly dead entries (hard expiry)
			if now.After(v.deadAt) {
				delete(c.memData, k)
			}
		}
		c.mu.Unlock()
	}
}

// Get retrieves a value from cache. Returns (found, stale).
// If stale=true, the data is still valid but should be refreshed in the background.
func (c *Cache) Get(ctx context.Context, key string, dest interface{}) bool {
	if c.useRedis {
		data, err := c.redis.Get(ctx, key).Bytes()
		if err != nil {
			return false
		}
		return json.Unmarshal(data, dest) == nil
	}

	// In-memory
	c.mu.RLock()
	entry, ok := c.memData[key]
	c.mu.RUnlock()

	if !ok || time.Now().After(entry.deadAt) {
		return false
	}

	return json.Unmarshal(entry.data, dest) == nil
}

// IsStale checks if a cached entry exists but is past its soft expiry
func (c *Cache) IsStale(ctx context.Context, key string) bool {
	if c.useRedis {
		// Redis handles expiry itself; treat as not stale
		return false
	}

	c.mu.RLock()
	entry, ok := c.memData[key]
	c.mu.RUnlock()

	if !ok {
		return false
	}

	return time.Now().After(entry.expiresAt)
}

// Set stores a value in cache
func (c *Cache) Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	if ttl == 0 {
		ttl = c.defaultTTL
	}

	data, err := json.Marshal(value)
	if err != nil {
		return err
	}

	if c.useRedis {
		return c.redis.Set(ctx, key, data, ttl).Err()
	}

	// In-memory: soft expiry at TTL, hard expiry at 2x TTL (stale-while-revalidate window)
	c.mu.Lock()
	c.memData[key] = cacheEntry{
		data:      data,
		expiresAt: time.Now().Add(ttl),
		deadAt:    time.Now().Add(ttl * 2),
	}
	c.mu.Unlock()

	return nil
}

// TryStartRefresh attempts to claim a refresh lock for a key.
// Returns true if this caller should perform the refresh, false if another goroutine is already refreshing.
func (c *Cache) TryStartRefresh(key string) bool {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()

	if c.refreshing[key] {
		return false // Someone else is already refreshing
	}
	c.refreshing[key] = true
	return true
}

// FinishRefresh releases the refresh lock for a key
func (c *Cache) FinishRefresh(key string) {
	c.refreshMu.Lock()
	delete(c.refreshing, key)
	c.refreshMu.Unlock()
}

// Delete removes a key from cache
func (c *Cache) Delete(ctx context.Context, key string) error {
	if c.useRedis {
		return c.redis.Del(ctx, key).Err()
	}

	c.mu.Lock()
	delete(c.memData, key)
	c.mu.Unlock()
	return nil
}

// InvalidateDashboard clears all dashboard, scripts and errors cache keys
func (c *Cache) InvalidateDashboard(ctx context.Context) {
	prefixes := []string{"dashboard:", "scripts:", "errors:"}
	if c.useRedis {
		for _, prefix := range prefixes {
			iter := c.redis.Scan(ctx, 0, prefix+"*", 100).Iterator()
			for iter.Next(ctx) {
				c.redis.Del(ctx, iter.Val())
			}
		}
		return
	}

	c.mu.Lock()
	for k := range c.memData {
		for _, prefix := range prefixes {
			if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
				delete(c.memData, k)
				break
			}
		}
	}
	c.mu.Unlock()
}
