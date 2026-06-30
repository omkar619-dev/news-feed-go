// Package mq wraps RabbitMQ: the web server publishes "post created" events,
// and the worker consumes them to do fan-out asynchronously.
package mq

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// fanoutQueue is the single queue we use for "a post was created" events.
const fanoutQueue = "post.fanout"

// PostCreatedEvent is the tiny message we put on the queue when a post is made.
// Note how small it is — just two ids. The worker uses these to do the fan-out.
type PostCreatedEvent struct {
	PostID   int64 `json:"post_id"`
	AuthorID int64 `json:"author_id"`
}

// Client holds the RabbitMQ connection + channel. (A "channel" is a lightweight
// virtual connection multiplexed over the single TCP connection — AMQP work
// happens on channels, not directly on the connection.)
type Client struct {
	conn *amqp.Connection
	ch   *amqp.Channel
}

// dialAttempts/dialDelay bound how long we keep retrying the initial connection.
// Total wait ≈ dialAttempts * dialDelay (here ~180s) — generous enough to cover
// RabbitMQ's SLOW cold boot on weak hardware. On the Old PC (Core-2-Duo) the
// broker took ~3min to accept connections, which outran the old 40s window and
// caused a one-time worker/relay Error+restart. 180s rides over it cleanly.
const (
	dialAttempts = 90
	dialDelay    = 2 * time.Second
)

// dialWithRetry tries to connect several times before giving up. Why: a freshly
// started broker isn't ready instantly, so the FIRST dial right after
// `docker compose up` often fails with a transient handshake error (the EOF you
// saw). Retrying rides over the startup window instead of crashing the app on a
// slow-but-healthy dependency. (Production often uses *exponential* backoff —
// 1s, 2s, 4s… — but a fixed short delay is plenty here and easier to read.)
func dialWithRetry(url string) (*amqp.Connection, error) {
	var lastErr error
	for attempt := 1; attempt <= dialAttempts; attempt++ {
		conn, err := amqp.Dial(url)
		if err == nil {
			return conn, nil // connected
		}
		lastErr = err
		log.Printf("rabbitmq not ready (attempt %d/%d): %v — retrying in %s", attempt, dialAttempts, err, dialDelay)
		time.Sleep(dialDelay)
	}
	return nil, fmt.Errorf("mq dial: gave up after %d attempts: %w", dialAttempts, lastErr)
}

// New dials RabbitMQ (with retry) and declares the queue. Declaring is idempotent
// and done by BOTH publisher and worker, so whoever starts first creates it.
func New(url string) (*Client, error) {
	conn, err := dialWithRetry(url)
	if err != nil {
		return nil, err
	}
	ch, err := conn.Channel()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("mq channel: %w", err)
	}
	// QueueDeclare(name, durable, autoDelete, exclusive, noWait, args).
	// durable=true → the queue definition survives a broker restart.
	if _, err := ch.QueueDeclare(fanoutQueue, true, false, false, false, nil); err != nil {
		ch.Close()
		conn.Close()
		return nil, fmt.Errorf("mq declare queue: %w", err)
	}
	return &Client{conn: conn, ch: ch}, nil
}

// PublishPostCreated drops a PostCreatedEvent onto the queue. This is the tiny,
// instant operation the web server does instead of the slow fan-out.
func (c *Client) PublishPostCreated(ctx context.Context, evt PostCreatedEvent) error {
	body, err := json.Marshal(evt)
	if err != nil {
		return err
	}
	// Publish(exchange="", routingKey=queueName, ...) — the empty/default
	// exchange routes a message straight to the queue named by the routing key.
	return c.ch.PublishWithContext(ctx, "", fanoutQueue, false, false, amqp.Publishing{
		ContentType:  "application/json",
		Body:         body,
		DeliveryMode: amqp.Persistent, // message itself is saved to disk → survives a broker restart
	})
}

// Consume blocks, calling handler for each message. ack/nack gives us
// at-least-once delivery: we only remove a message after handler succeeds.
func (c *Client) Consume(handler func(PostCreatedEvent) error) error {
	// Consume(queue, consumer, autoAck, exclusive, noLocal, noWait, args).
	// autoAck=false → WE decide when a message is "done" (after handler runs).
	msgs, err := c.ch.Consume(fanoutQueue, "", false, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("mq consume: %w", err)
	}
	for msg := range msgs {
		var evt PostCreatedEvent
		if err := json.Unmarshal(msg.Body, &evt); err != nil {
			// Unparseable message — drop it (requeue=false) so it doesn't loop forever.
			_ = msg.Nack(false, false)
			continue
		}
		if err := handler(evt); err != nil {
			// Handler failed — requeue (requeue=true) so we retry it later.
			_ = msg.Nack(false, true)
			continue
		}
		// Success — acknowledge, removing it from the queue.
		_ = msg.Ack(false)
	}
	return nil
}

// Close tears down the channel and connection.
func (c *Client) Close() {
	if c.ch != nil {
		_ = c.ch.Close()
	}
	if c.conn != nil {
		_ = c.conn.Close()
	}
}
