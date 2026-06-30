-- name: CreateComment :one
-- parent_id is NULL for a top-level comment, or another comment's id for a reply.
INSERT INTO comments (post_id, author_id, parent_id, content)
VALUES ($1, $2, $3, $4)
RETURNING id, post_id, author_id, parent_id, content, created_at;

-- name: GetCommentTree :many
-- Fetch a post's ENTIRE comment tree (all nesting levels) with a recursive CTE.
WITH RECURSIVE thread AS (
    -- ANCHOR: top-level comments (no parent) on this post.
    SELECT c.id, c.post_id, c.author_id, c.parent_id, c.content, c.created_at,
           1 AS depth,
           ARRAY[c.id] AS path
    FROM comments c
    WHERE c.post_id = $1 AND c.parent_id IS NULL

    UNION ALL

    -- RECURSIVE: replies to comments we've already collected. Repeats until
    -- no new replies are found. depth +1 per level; path appends this id so we
    -- can order the result depth-first (each reply sits under its parent).
    SELECT c.id, c.post_id, c.author_id, c.parent_id, c.content, c.created_at,
           t.depth + 1,
           t.path || c.id
    FROM comments c
    JOIN thread t ON c.parent_id = t.id
)
SELECT id, post_id, author_id, parent_id, content, created_at, depth
FROM thread
ORDER BY path;

-- name: DeleteComment :one
-- Owner-only. Cascade removes the comment's whole reply subtree.
-- RETURNING tells us if a row was actually deleted (else ErrNoRows → 404).
DELETE FROM comments
WHERE id = $1 AND author_id = $2
RETURNING id;