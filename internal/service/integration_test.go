//go:build integration

// This file compiles ONLY under `-tags=integration`, so the default `go test`
// (and the CI unit job) stays fast and needs no infrastructure. Run it with the
// real dependencies up:
//
//	docker compose up -d
//	go test -tags=integration -run Integration ./internal/service/... -v
package service

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omkar619-dev/news-feed-go/internal/cache"
	"github.com/omkar619-dev/news-feed-go/internal/repository/postgres/sqlc"
)

// envOr returns the env var, or def when it's unset — so the test targets the
// docker-compose defaults locally but honours overrides in CI.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// TestLikeHotCounterIntegration covers the ONE path the unit tests can't reach: the
// like hot-counter, which spans REAL Postgres (the likes table + the COUNT(*) cold
// seed) AND REAL Redis (the counter). It walks the full like → idempotent re-like →
// unlike cycle and asserts the count at each step.
func TestLikeHotCounterIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	dsn := envOr("DATABASE_URL", "postgres://newsfeed:newsfeed_dev@localhost:5432/newsfeed?sslmode=disable")
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("no Postgres pool (%v) — run `docker compose up -d`", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("Postgres not reachable (%v) — run `docker compose up -d`", err)
	}

	counter, err := cache.New(envOr("REDIS_ADDR", "localhost:6380"))
	if err != nil {
		t.Skipf("no Redis (%v) — run `docker compose up -d`", err)
	}
	defer counter.Close()

	q := sqlc.New(pool)

	// Seed a UNIQUE user (so repeated runs don't trip the username/email UNIQUE
	// constraints), then a post by them. Deleting the user at the end cascades to
	// the post and the like row (ON DELETE CASCADE), so the test cleans up after itself.
	uniq := time.Now().UnixNano()
	var userID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (username, email, password_hash)
		 VALUES ($1, $2, 'x') RETURNING id`,
		fmt.Sprintf("itest_%d", uniq), fmt.Sprintf("itest_%d@example.com", uniq),
	).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})

	post, err := q.CreatePost(ctx, sqlc.CreatePostParams{AuthorID: userID, Content: "integration test post"})
	if err != nil {
		t.Fatalf("seed post: %v", err)
	}

	// 1. Like → the cold counter seeds from COUNT(*) (= 1 after the insert) → 1.
	res, err := Like(ctx, q, counter, userID, post.ID)
	if err != nil {
		t.Fatalf("Like: %v", err)
	}
	if !res.Liked || res.Count != 1 {
		t.Fatalf("after Like: {Liked:%v Count:%d}, want {true 1}", res.Liked, res.Count)
	}

	// 2. Like AGAIN → ON CONFLICT DO NOTHING → no real change → count stays 1.
	res, err = Like(ctx, q, counter, userID, post.ID)
	if err != nil {
		t.Fatalf("Like (repeat): %v", err)
	}
	if res.Count != 1 {
		t.Fatalf("after 2nd Like: count = %d, want 1 (idempotent)", res.Count)
	}

	// 3. Unlike → the warm counter is decremented → back to 0.
	res, err = Unlike(ctx, q, counter, userID, post.ID)
	if err != nil {
		t.Fatalf("Unlike: %v", err)
	}
	if res.Liked || res.Count != 0 {
		t.Fatalf("after Unlike: {Liked:%v Count:%d}, want {false 0}", res.Liked, res.Count)
	}
}
