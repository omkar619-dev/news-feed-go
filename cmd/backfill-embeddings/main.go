// Command backfill-embeddings embeds EVERY post currently in the DB and upserts
// its vector into post_embeddings.
//
// Why this exists: posts inserted directly via SQL (like the eval corpus in
// eval/seed.sql) never published a post.created event, so the worker never
// embedded them — semantic search would be blind to them. This one-shot tool
// fills the gap. It's idempotent (UpsertPostEmbedding is ON CONFLICT DO UPDATE),
// so it's also what you'd run after swapping embedding models.
//
// Run with Ollama up:
//   go run ./cmd/backfill-embeddings
package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/pgvector/pgvector-go"

	"github.com/omkar619-dev/news-feed-go/internal/db"
	"github.com/omkar619-dev/news-feed-go/internal/embed"
	"github.com/omkar619-dev/news-feed-go/internal/repository/postgres/sqlc"
)

func main() {
	ctx := context.Background()

	pool, err := db.New(ctx)
	if err != nil {
		log.Fatalf("db setup failed: %v", err)
	}
	defer pool.Close()
	queries := sqlc.New(pool)

	ollamaURL := os.Getenv("OLLAMA_URL")
	if ollamaURL == "" {
		ollamaURL = "http://localhost:11434"
	}
	embedder := embed.New(ollamaURL, "all-minilm")

	posts, err := queries.ListPostsForEmbedding(ctx)
	if err != nil {
		log.Fatalf("listing posts failed: %v", err)
	}
	log.Printf("embedding %d posts...", len(posts))

	var ok, failed int
	for _, p := range posts {
		// Each embedding gets its own short timeout so one slow call can't hang
		// the whole backfill.
		ec, cancel := context.WithTimeout(ctx, 30*time.Second)
		vec, err := embedder.Embed(ec, p.Content)
		cancel()
		if err != nil {
			log.Printf("post %d: embed failed: %v (skipping)", p.ID, err)
			failed++
			continue
		}

		if err := queries.UpsertPostEmbedding(ctx, sqlc.UpsertPostEmbeddingParams{
			PostID:    p.ID,
			Embedding: pgvector.NewVector(vec),
		}); err != nil {
			log.Printf("post %d: upsert failed: %v", p.ID, err)
			failed++
			continue
		}
		ok++
	}

	log.Printf("done: %d embedded, %d failed", ok, failed)
}
