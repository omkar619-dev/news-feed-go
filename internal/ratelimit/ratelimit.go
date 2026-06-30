// Package ratelimit is a Redis-backed sliding-window rate limiter + middleware.
package ratelimit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/omkar619-dev/news-feed-go/internal/auth"
)

// slidingWindowScript runs the trim → count → add steps ATOMICALLY in Redis.
//   KEYS[1] = the per-user key (e.g. "ratelimit:post:42")
//   ARGV[1] = now    (unix MILLIS — ms fits in Lua's float64; ns would not)
//   ARGV[2] = window (in millis)
//   ARGV[3] = limit  (max requests allowed within the window)
//   ARGV[4] = a unique member for THIS request
// Returns 1 if allowed, 0 if over the limit.
var slidingWindowScript = redis.NewScript(`
local now    = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local limit  = tonumber(ARGV[3])
local member = ARGV[4]

-- 1. SLIDE: drop every entry older than (now - window)
redis.call('ZREMRANGEBYSCORE', KEYS[1], 0, now - window)

-- 2. COUNT what's left inside the window
local count = redis.call('ZCARD', KEYS[1])
if count >= limit then
  return 0          -- already at the cap → deny
end

-- 3. ADD this request, and refresh the key's expiry so it self-cleans
redis.call('ZADD', KEYS[1], now, member)
redis.call('PEXPIRE', KEYS[1], window)
return 1            -- allowed
`)

// Limiter enforces "at most `limit` events per `window`" per key.
type Limiter struct {
	rdb    *redis.Client
	limit  int
	window time.Duration
}

// New builds a limiter. e.g. New(rdb, 5, time.Minute) = 5 events / minute / key.
func New(rdb *redis.Client, limit int, window time.Duration) *Limiter {
	return &Limiter{rdb: rdb, limit: limit, window: window}
}

// Allow reports whether an event under `key` is permitted right now.
func (l *Limiter) Allow(ctx context.Context, key string) (bool, error) {
	now := time.Now().UnixMilli()
	member, err := uniqueMember(now)
	if err != nil {
		return false, err
	}
	res, err := slidingWindowScript.Run(
		ctx, l.rdb, []string{key},
		now, l.window.Milliseconds(), l.limit, member,
	).Int()
	if err != nil {
		return false, err
	}
	return res == 1, nil
}

// uniqueMember makes each request a DISTINCT ZSET member, so two requests in the
// same millisecond don't collide into one entry: "<now>-<random hex>".
func uniqueMember(now int64) (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return strconv.FormatInt(now, 10) + "-" + hex.EncodeToString(b), nil
}

// Middleware returns chi/net-http middleware that rate-limits PER AUTHENTICATED
// USER. Mount it AFTER auth (so the userID is in context). keyPrefix namespaces
// the limit, e.g. "post" → key "ratelimit:post:<userID>".
func (l *Limiter) Middleware(keyPrefix string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			userID, ok := auth.UserIDFromContext(r.Context())
			if !ok {
				// No user (shouldn't happen behind RequireAuth) — let it through.
				next.ServeHTTP(w, r)
				return
			}

			key := fmt.Sprintf("ratelimit:%s:%d", keyPrefix, userID)
			allowed, err := l.Allow(r.Context(), key)
			if err != nil {
				// FAIL-OPEN: if Redis is down, don't block the user — a flaky
				// cache shouldn't take writes offline. (Fail-closed is the other
				// choice; depends whether you value availability or strictness.)
				log.Printf("rate limiter error for %s: %v", key, err)
				next.ServeHTTP(w, r)
				return
			}

			if !allowed {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests) // 429
				json.NewEncoder(w).Encode(map[string]string{"error": "too many requests — slow down"})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
