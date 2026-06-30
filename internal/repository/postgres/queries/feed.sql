-- name: FanOutPostToFollowers :exec
-- Fan-out-on-WRITE: push a freshly-created post into the precomputed timeline
-- of every follower of its author. INSERT ... SELECT writes one row per
-- follower in a single statement. ON CONFLICT DO NOTHING = idempotent.
-- (Skipped at the call site when the author is a celebrity.)
INSERT INTO timelines (user_id, post_id)
SELECT f.follower_id, sqlc.arg(post_id)
FROM follows f
WHERE f.followee_id = sqlc.arg(author_id)
ON CONFLICT (user_id, post_id) DO NOTHING;

-- name: GetFeedFirst :many
-- HYBRID feed, first page. Merges TWO sources with UNION:
--   (a) my precomputed timeline — posts pushed by NORMAL users I follow
--   (b) live posts by CELEBRITIES I follow — fetched at read time (not pushed)
-- UNION (not UNION ALL) de-duplicates if a post appears in both.
SELECT id, author_id, content, created_at
FROM (
    SELECT p.id, p.author_id, p.content, p.created_at
    FROM timelines t
    JOIN posts p ON p.id = t.post_id
    WHERE t.user_id = sqlc.arg(user_id)
    UNION
    SELECT p.id, p.author_id, p.content, p.created_at
    FROM follows f
    JOIN users u ON u.id = f.followee_id
    JOIN posts p ON p.author_id = u.id
    WHERE f.follower_id = sqlc.arg(user_id) AND u.is_celebrity = true
) feed
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(page_limit);

-- name: GetFeedAfter :many
-- HYBRID feed, next page. Same UNION, with the (created_at, id) keyset cursor
-- applied to the MERGED result in the outer query.
SELECT id, author_id, content, created_at
FROM (
    SELECT p.id, p.author_id, p.content, p.created_at
    FROM timelines t
    JOIN posts p ON p.id = t.post_id
    WHERE t.user_id = sqlc.arg(user_id)
    UNION
    SELECT p.id, p.author_id, p.content, p.created_at
    FROM follows f
    JOIN users u ON u.id = f.followee_id
    JOIN posts p ON p.author_id = u.id
    WHERE f.follower_id = sqlc.arg(user_id) AND u.is_celebrity = true
) feed
WHERE (feed.created_at, feed.id) < (sqlc.arg(cursor_created_at)::timestamptz, sqlc.arg(cursor_id)::bigint)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(page_limit);
