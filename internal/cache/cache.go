// Package cache is a thin wrapper over Redis for cache-aside reads.
package cache

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// Client wraps a Redis connection.
type Client struct {
	rdb *redis.Client
}

// New connects to Redis at addr ("host:port") and pings to verify it's reachable.
func New(addr string) (*Client, error) {
	rdb := redis.NewClient(&redis.Options{Addr: addr})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, err
	}
	return &Client{rdb: rdb}, nil
}

// Redis exposes the underlying *redis.Client so other packages (e.g. the rate
// limiter) can share this same connection pool instead of opening their own.
func (c *Client) Redis() *redis.Client {
	return c.rdb
}

// Get returns (value, found, error). found is false on a cache MISS — note we
// translate redis.Nil (the "key doesn't exist" sentinel) into found=false
// rather than an error, because a miss is normal, not a failure.
func (c *Client) Get(ctx context.Context, key string) (string, bool, error) {
	val, err := c.rdb.Get(ctx, key).Result()
	if err == redis.Nil {
		return "", false, nil // MISS — key not present
	}
	if err != nil {
		return "", false, err // a real Redis error
	}
	return val, true, nil // HIT
}

// Set stores key=value with a TTL (after which Redis auto-deletes it). The TTL
// is what bounds staleness in our cache-aside design.
func (c *Client) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	return c.rdb.Set(ctx, key, value, ttl).Err()
}

// Del removes a key (used for active invalidation, if/when we add it).
func (c *Client) Del(ctx context.Context, key string) error {
	return c.rdb.Del(ctx, key).Err()
}

// Incr atomically increments key (creating it at 1 if absent) and returns the new value.
func (c *Client) Incr(ctx context.Context, key string) (int64, error) {
	return c.rdb.Incr(ctx, key).Result()
}

// Decr atomically decrements key and returns the new value.
func (c *Client) Decr(ctx context.Context, key string) (int64, error) {
	return c.rdb.Decr(ctx, key).Result()
}

// GetInt returns (value, found, error). found is false on a MISS (redis.Nil).
func (c *Client) GetInt(ctx context.Context, key string) (int64, bool, error) {
	v, err := c.rdb.Get(ctx, key).Int64()
	if err == redis.Nil {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return v, true, nil
}

// Close releases the connection pool.
func (c *Client) Close() error {
	return c.rdb.Close()
}
