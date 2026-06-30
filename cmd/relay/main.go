// Command relay is the transactional-outbox relay. It polls the `outbox` table
// for unpublished events and publishes them to RabbitMQ, then stamps each as
// published. This is the half of the outbox pattern that makes POST /posts
// crash-safe: the API only writes the event to the DB (atomically with the post);
// this process is what actually gets it to the broker.
//
// Delivery is AT-LEAST-ONCE: if we publish an event but die before marking it
// published, we'll publish it again next loop. That's fine — the worker is
// idempotent (fan-out is ON CONFLICT DO NOTHING, embedding is an upsert), so a
// duplicate event does no harm.
//
// Run it alongside the api and the worker (three processes):
//   go run ./cmd/relay
package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omkar619-dev/news-feed-go/internal/db"
	"github.com/omkar619-dev/news-feed-go/internal/mq"
	"github.com/omkar619-dev/news-feed-go/internal/repository/postgres/sqlc"
)

const (
	batchSize    = 50              // events claimed per poll
	pollInterval = 1 * time.Second // wait between polls when the outbox is empty
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	pool, err := db.New(ctx)
	if err != nil {
		log.Fatalf("db setup failed: %v", err)
	}
	defer pool.Close()

	amqpURL := os.Getenv("RABBITMQ_URL")
	if amqpURL == "" {
		amqpURL = "amqp://newsfeed:newsfeed_dev@localhost:5673/"
	}
	publisher, err := mq.New(amqpURL)
	if err != nil {
		log.Fatalf("mq setup failed: %v", err)
	}
	defer publisher.Close()

	log.Println("outbox relay started")

	for {
		select {
		case <-ctx.Done():
			log.Println("relay shutting down")
			return
		default:
		}

		n, err := relayOnce(ctx, pool, publisher)
		if err != nil {
			log.Printf("relay batch error: %v", err)
		}

		// If we drained a full batch there may be more queued, so loop again
		// immediately. Otherwise the outbox is (near) empty — back off a beat.
		if n < batchSize {
			select {
			case <-ctx.Done():
				return
			case <-time.After(pollInterval):
			}
		}
	}
}

// relayOnce claims a batch of unpublished events, publishes each to the broker,
// and marks it published — all inside ONE transaction. Keeping it in a single
// transaction means the FOR UPDATE SKIP LOCKED locks are held until we're done,
// so no other relay can grab the same rows. Returns how many events it handled.
//
// If a publish fails partway, we return the error and the deferred Rollback undoes
// the whole batch — every row stays unpublished and is retried next loop. Some may
// then publish twice; the idempotent worker absorbs the duplicates.
func relayOnce(ctx context.Context, pool *pgxpool.Pool, publisher *mq.Client) (int, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	qtx := sqlc.New(tx)
	events, err := qtx.FetchUnpublishedOutbox(ctx, batchSize)
	if err != nil {
		return 0, err
	}

	for _, evt := range events {
		switch evt.EventType {
		case "post.created":
			var pc mq.PostCreatedEvent
			if err := json.Unmarshal(evt.Payload, &pc); err != nil {
				// A malformed payload can never succeed; retrying it forever would
				// wedge the outbox. Log and mark it published (give up on it).
				log.Printf("outbox %d: bad payload, skipping: %v", evt.ID, err)
			} else if err := publisher.PublishPostCreated(ctx, pc); err != nil {
				// Broker problem — abort the batch. Rollback leaves the rows
				// unpublished and we retry next loop. Nothing is lost.
				return 0, err
			}
		default:
			log.Printf("outbox %d: unknown event_type %q, skipping", evt.ID, evt.EventType)
		}

		if err := qtx.MarkOutboxPublished(ctx, evt.ID); err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(events), nil
}
