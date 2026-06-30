-- name: InsertOutboxEvent :exec
-- Write a domain event into the outbox. Called in the SAME transaction as the
-- state change (e.g. creating a post), so the event and the change commit
-- atomically — both or neither. This is the heart of the transactional outbox.
INSERT INTO outbox (event_type, payload)
VALUES (sqlc.arg(event_type), sqlc.arg(payload));

-- name: FetchUnpublishedOutbox :many
-- The relay claims a batch of not-yet-published events, oldest first.
-- FOR UPDATE SKIP LOCKED row-locks the claimed rows and skips any a different
-- relay already holds — so you can run several relays at once and no two of them
-- will ever grab the same event.
SELECT id, event_type, payload, created_at, published_at
FROM outbox
WHERE published_at IS NULL
ORDER BY id
LIMIT sqlc.arg(batch_size)
FOR UPDATE SKIP LOCKED;

-- name: MarkOutboxPublished :exec
-- Stamp an event as published, once the relay has handed it to the broker.
UPDATE outbox SET published_at = NOW() WHERE id = sqlc.arg(id);
