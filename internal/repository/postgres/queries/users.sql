-- name: CreateUser :one
INSERT INTO users (
    username,
    email,
    password_hash
)
VALUES ($1, $2, $3)
RETURNING id, username, email, created_at;

-- name: GetUserByEmail :one
SELECT id, username, email, password_hash, created_at
FROM users
WHERE email = $1
LIMIT 1;

-- name: GetUserByID :one
SELECT id, username, email, created_at
FROM users
WHERE id = $1
LIMIT 1;

-- name: GetUserByUsername :one
SELECT id, username, email, created_at
FROM users
WHERE username = $1
LIMIT 1;

-- name: DeleteUser :exec
DELETE FROM users WHERE id = $1;

-- name: IsCelebrity :one
-- Used on the write path: if the author is a celebrity, we SKIP fan-out
-- (don't push to millions of timelines) and rely on read-time merge instead.
SELECT is_celebrity FROM users WHERE id = $1;

-- name: GetProfileByUsername :one
-- Public profile: the user's basic info PLUS live counts, all in one query.
-- Each (SELECT COUNT(*) ...) is a correlated scalar subquery — it re-runs for
-- the matched user u, referencing u.id from the outer query.
SELECT
    u.id,
    u.username,
    u.created_at,
    (SELECT COUNT(*) FROM follows f WHERE f.followee_id = u.id) AS followers_count,
    (SELECT COUNT(*) FROM follows f WHERE f.follower_id = u.id) AS following_count,
    (SELECT COUNT(*) FROM posts   p WHERE p.author_id  = u.id) AS posts_count
FROM users u
WHERE u.username = $1
LIMIT 1;