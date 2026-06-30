// Command worker is the CONSUMER ("cook"): it pulls "post created" events off
// the RabbitMQ queue and, in the background, (1) fans the post out to followers
// and (2) generates + stores the post's embedding for semantic search.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/pgvector/pgvector-go"

	"github.com/omkar619-dev/news-feed-go/internal/db"
	"github.com/omkar619-dev/news-feed-go/internal/embed"
	"github.com/omkar619-dev/news-feed-go/internal/logging"
	"github.com/omkar619-dev/news-feed-go/internal/mq"
	"github.com/omkar619-dev/news-feed-go/internal/repository/postgres/sqlc"
)

func main() {
	logging.Setup() // JSON structured logs for everything below

	rootCtx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Same DB pool setup as the web server — the worker also talks to Postgres.
	pool, err := db.New(rootCtx)
	if err != nil {
		slog.Error("db setup failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	queries := sqlc.New(pool)

	amqpURL := os.Getenv("RABBITMQ_URL")
	if amqpURL == "" {
		amqpURL = "amqp://newsfeed:newsfeed_dev@localhost:5673/"
	}
	client, err := mq.New(amqpURL)
	if err != nil {
		slog.Error("mq setup failed", "err", err)
		os.Exit(1)
	}
	defer client.Close()

	// Embedding model client (Ollama, running locally).
	ollamaURL := os.Getenv("OLLAMA_URL")
	if ollamaURL == "" {
		ollamaURL = "http://localhost:11434"
	}
	embedder := embed.New(ollamaURL, "all-minilm")

	// Graceful shutdown: closing the mq client ends the Consume loop below.
	go func() {
		<-rootCtx.Done()
		slog.Info("shutdown signal received, closing worker")
		client.Close()
	}()

	// handler runs for EACH event. Returning an error requeues the message for a
	// retry; returning nil acknowledges it as done.
	handler := func(evt mq.PostCreatedEvent) error {
		// ── (A) Fan-out (skip for celebrities — they're merged in at read time) ──
		isCeleb, err := queries.IsCelebrity(rootCtx, evt.AuthorID)
		if err != nil {
			slog.Warn("is_celebrity check failed", "user_id", evt.AuthorID, "err", err)
			isCeleb = false // default to fanning out
		}
		if isCeleb {
			slog.Info("skipped fan-out (celebrity)", "post_id", evt.PostID)
		} else if err := queries.FanOutPostToFollowers(rootCtx, sqlc.FanOutPostToFollowersParams{
			PostID:   evt.PostID,
			AuthorID: evt.AuthorID,
		}); err != nil {
			// Fan-out is critical → requeue to retry (idempotent, so safe).
			slog.Error("fan-out failed", "post_id", evt.PostID, "err", err)
			return err
		} else {
			slog.Info("fanned out post", "post_id", evt.PostID, "author_id", evt.AuthorID)
		}

		// ── (B) Embedding (ALWAYS — so every post is searchable). Best-effort. ──
		postRow, err := queries.GetPostByID(rootCtx, evt.PostID)
		if err != nil {
			slog.Error("embed: could not load post", "post_id", evt.PostID, "err", err)
			return nil
		}
		vec, err := embedder.Embed(rootCtx, postRow.Content)
		if err != nil {
			slog.Error("embed failed", "post_id", evt.PostID, "err", err)
			return nil
		}
		if err := queries.UpsertPostEmbedding(rootCtx, sqlc.UpsertPostEmbeddingParams{
			PostID:    evt.PostID,
			Embedding: pgvector.NewVector(vec),
		}); err != nil {
			slog.Error("store embedding failed", "post_id", evt.PostID, "err", err)
			return nil
		}
		slog.Info("embedded post", "post_id", evt.PostID)
		return nil
	}

	slog.Info("worker started, waiting for post-created events")
	if err := client.Consume(handler); err != nil {
		slog.Error("consume stopped", "err", err)
	}
	slog.Info("worker stopped")
}
