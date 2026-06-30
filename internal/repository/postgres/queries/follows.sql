-- name: FollowUser :exec
-- Idempotent: ON CONFLICT DO NOTHING means following someone you ALREADY
-- follow is a successful no-op rather than a duplicate-key error.
INSERT INTO follows (follower_id, followee_id)
VALUES ($1, $2)
ON CONFLICT (follower_id, followee_id) DO NOTHING;

-- name: UnfollowUser :exec
-- Idempotent too: deleting a follow that isn't there affects 0 rows, no error.
DELETE FROM follows
WHERE follower_id = $1 AND followee_id = $2;

-- name: ListFollowers :many
-- "Who follows user {id}?" — {id} is the FOLLOWEE; we want each FOLLOWER's
-- public info. So filter on followee_id, join users on follower_id.
-- Only id + username are returned (no email/PII on a public endpoint).
SELECT u.id, u.username
FROM follows f
JOIN users u ON u.id = f.follower_id
WHERE f.followee_id = $1
ORDER BY f.created_at DESC
LIMIT 100;

-- name: ListFollowing :many
-- "Who does user {id} follow?" — {id} is the FOLLOWER; we want each FOLLOWEE's
-- public info. So filter on follower_id, join users on followee_id.
SELECT u.id, u.username
FROM follows f
JOIN users u ON u.id = f.followee_id
WHERE f.follower_id = $1
ORDER BY f.created_at DESC
LIMIT 100;
