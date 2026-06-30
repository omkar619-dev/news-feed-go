// Package service holds the feed's DOMAIN logic — the operations that are true
// regardless of how they're triggered. Both the HTTP handlers and the SSH TUI
// call these functions, so each business rule (idempotent likes, the hot-counter
// seed, ownership checks, the outbox write) lives in exactly ONE place. This is
// the "core" in a ports-and-adapters design; HTTP and SSH are just adapters.
package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/omkar619-dev/news-feed-go/internal/cache"
	"github.com/omkar619-dev/news-feed-go/internal/repository/postgres/sqlc"
)

// ErrPostNotFound is a SENTINEL error: a named error value the caller tests for
// with errors.Is. Adapters translate it — HTTP → 404, the TUI → a status line —
// so the service never needs to know about HTTP status codes.
var ErrPostNotFound = errors.New("post not found")

// LikeResult is the domain outcome of a like/unlike: the new state + fresh count.
// No json tags — it's a domain type, not a wire format; each adapter maps it.
type LikeResult struct {
	Liked bool
	Count int64
}

func likeCounterKey(postID int64) string {
	return fmt.Sprintf("likes:%d", postID)
}

// Like records that userID likes postID (idempotent), then returns the count.
// q is a Querier, so it works whether the caller passes the pool or a tx.
func Like(ctx context.Context, q sqlc.Querier, counter *cache.Client, userID, postID int64) (LikeResult, error) {
	delta := 0
	_, err := q.LikePost(ctx, sqlc.LikePostParams{UserID: userID, PostID: postID})
	switch {
	case err == nil:
		delta = 1 // a NEW like row was inserted
	case errors.Is(err, pgx.ErrNoRows):
		delta = 0 // already liked → ON CONFLICT DO NOTHING → no-op
	default:
		// 23503 = foreign-key violation → the post doesn't exist.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return LikeResult{}, ErrPostNotFound
		}
		return LikeResult{}, err
	}

	count, err := likeCount(ctx, counter, q, postID, delta)
	if err != nil {
		return LikeResult{}, err
	}
	return LikeResult{Liked: true, Count: count}, nil
}

// Unlike removes userID's like on postID (idempotent), then returns the count.
func Unlike(ctx context.Context, q sqlc.Querier, counter *cache.Client, userID, postID int64) (LikeResult, error) {
	delta := 0
	_, err := q.UnlikePost(ctx, sqlc.UnlikePostParams{UserID: userID, PostID: postID})
	switch {
	case err == nil:
		delta = -1 // a like was actually removed
	case errors.Is(err, pgx.ErrNoRows):
		delta = 0 // wasn't liked → no-op
	default:
		return LikeResult{}, err
	}

	count, err := likeCount(ctx, counter, q, postID, delta)
	if err != nil {
		return LikeResult{}, err
	}
	return LikeResult{Liked: false, Count: count}, nil
}

// likeCount returns the post's like count, applying delta to the Redis counter.
// If the counter is COLD (missing), it seeds from the durable likes table — which
// already reflects the committed change — instead of blindly INCR-ing (which would
// undercount). This hot-counter logic is now shared by every caller.
func likeCount(ctx context.Context, counter *cache.Client, q sqlc.Querier, postID int64, delta int) (int64, error) {
	key := likeCounterKey(postID)
	cur, found, _ := counter.GetInt(ctx, key)
	if found {
		switch {
		case delta > 0:
			return counter.Incr(ctx, key)
		case delta < 0:
			return counter.Decr(ctx, key)
		default:
			return cur, nil // warm, no change → return current
		}
	}
	// Cold (or Redis error): the TABLE is truth and already includes any committed
	// change, so seed the counter from COUNT(*).
	n, err := q.CountPostLikes(ctx, postID)
	if err != nil {
		return 0, err
	}
	_ = counter.Set(ctx, key, strconv.FormatInt(n, 10), 0) // ttl 0 = persistent
	return n, nil
}
