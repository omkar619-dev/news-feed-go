-- name: GetIdempotentResponse :one
-- Has this (user, key) already completed? If so, return the stored response so we
-- can replay it instead of doing the work again.
SELECT response_status, response_body
FROM idempotency_keys
WHERE user_id = sqlc.arg(user_id) AND key = sqlc.arg(key);

-- name: InsertIdempotencyKey :exec
-- Record (user, key) -> response. Called INSIDE the post-creation transaction, so
-- a concurrent duplicate that already committed this key makes this INSERT fail
-- with a unique violation (23505) — rolling back the whole tx, post included.
INSERT INTO idempotency_keys (user_id, key, response_status, response_body)
VALUES (sqlc.arg(user_id), sqlc.arg(key), sqlc.arg(response_status), sqlc.arg(response_body));
