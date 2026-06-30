-- name: LikePost :one
-- Idempotent: ON CONFLICT DO NOTHING means liking twice is a no-op.
-- RETURNING post_id tells us whether a NEW like was actually inserted (a row
-- comes back) or it was already liked (no row → pgx.ErrNoRows). We'll use that
-- distinction later to only bump the Redis counter on a real state change.
INSERT INTO likes (user_id, post_id)
VALUES ($1, $2)
ON CONFLICT (user_id, post_id) DO NOTHING
RETURNING post_id;

-- name: UnlikePost :one
-- RETURNING tells us if a like was actually removed (row) vs wasn't there (ErrNoRows).
DELETE FROM likes
WHERE user_id = $1 AND post_id = $2
RETURNING post_id;

-- name: CountPostLikes :one
-- The simple, always-correct count. (Step 3 adds a Redis counter for hot reads.)
SELECT COUNT(*) FROM likes WHERE post_id = $1;