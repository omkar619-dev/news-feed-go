-- name: GetUserBySSHKey :one
-- Identify the SSH visitor by their public key. ErrNoRows = unregistered → guest.
SELECT u.id, u.username
FROM ssh_keys k
JOIN users u ON u.id = k.user_id
WHERE k.public_key = sqlc.arg(public_key);

-- name: ListRecentPosts :many
-- The global recent feed the TUI renders: post + author username + live like count.
SELECT p.id, p.author_id, u.username AS author, p.content, p.created_at,
       (SELECT COUNT(*) FROM likes l WHERE l.post_id = p.id) AS like_count
FROM posts p
JOIN users u ON u.id = p.author_id
ORDER BY p.created_at DESC
LIMIT sqlc.arg(result_limit);

-- name: RegisterSSHKey :exec
-- Claim a public key for a user (so future SSH visits identify them).
-- Idempotent re-claim: ON CONFLICT keeps the key pointed at the latest user.
INSERT INTO ssh_keys (public_key, user_id)
VALUES (sqlc.arg(public_key), sqlc.arg(user_id))
ON CONFLICT (public_key) DO UPDATE SET user_id = EXCLUDED.user_id;
