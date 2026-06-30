-- name: CreatePost :one
INSERT INTO posts (
    author_id,
    content
)
VALUES ($1, $2)
RETURNING id, author_id, content, created_at;

-- name: GetPostByID :one
SELECT id, author_id, content, created_at
FROM posts
WHERE id = $1
LIMIT 1;

-- name: DeletePost :exec
DELETE FROM posts
WHERE id = $1 AND author_id = $2;

-- name: ListUserPostsFirst :many
-- First page: the newest posts by an author. No cursor yet.
SELECT id, author_id, content, created_at
FROM posts
WHERE author_id = sqlc.arg(author_id)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(page_limit);

-- name: ListUserPostsAfter :many
-- Next page: posts strictly OLDER than the (created_at, id) bookmark.
-- The row-value comparison (created_at, id) < (cursor_time, cursor_id) is the
-- tuple/tiebreaker we discussed: id breaks ties when two posts share a time.
SELECT id, author_id, content, created_at
FROM posts
WHERE author_id = sqlc.arg(author_id)
  AND (created_at, id) < (sqlc.arg(cursor_created_at)::timestamptz, sqlc.arg(cursor_id)::bigint)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(page_limit);

-- name: ListPostsForEmbedding :many
-- Every post's id + content, for the embedding backfill tool. Posts inserted
-- directly via SQL (e.g. the eval corpus) never published a post.created event,
-- so the worker never embedded them — this lets us embed them after the fact.
SELECT id, content FROM posts ORDER BY id;
