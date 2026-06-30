-- name: UpsertPostEmbedding :exec
-- Store (or replace) a post's embedding. Upsert so re-processing the same post
-- (at-least-once delivery from the queue) just overwrites — idempotent.
INSERT INTO post_embeddings (post_id, embedding)
VALUES (sqlc.arg(post_id), sqlc.arg(embedding)::vector)
ON CONFLICT (post_id) DO UPDATE SET embedding = EXCLUDED.embedding;

-- name: SearchPostsByEmbedding :many
-- TWO-STAGE search: relevance is NOT the same as quality, so we don't just
-- return "most on-topic" — we return "on-topic AND fresh AND from someone with reach".
--   Stage 1 (inner): HNSW fetches the nearest ~100 candidates by PURE cosine
--                    distance — fast, index-accelerated.
--   Stage 2 (outer): re-rank ONLY those candidates by a blended score:
--                    relevance × recency-decay × author-authority.
SELECT id, author_id, content, created_at
FROM (
    SELECT
        p.id, p.author_id, p.content, p.created_at,
        (1 - (e.embedding <=> sqlc.arg(query_embedding)::vector)) AS relevance
    FROM posts p
    JOIN post_embeddings e ON e.post_id = p.id
    ORDER BY e.embedding <=> sqlc.arg(query_embedding)::vector -- HNSW-accelerated
    LIMIT 100 -- candidate pool to re-rank
) AS candidates
ORDER BY
    relevance
    * EXP(-EXTRACT(EPOCH FROM (NOW() - created_at)) / 604800.0)        -- recency: ~1-week decay
    * (1 + LN(1 + (SELECT COUNT(*) FROM likes l
                   WHERE l.post_id = candidates.id)))                   -- engagement (REAL likes)
    DESC
LIMIT sqlc.arg(result_limit);

-- name: GetRelatedPosts :many
-- "More like this": find posts nearest to a GIVEN post's own embedding,
-- excluding that post itself. The subquery fetches the target post's vector.
SELECT p.id, p.author_id, p.content, p.created_at
FROM posts p
JOIN post_embeddings e ON e.post_id = p.id
WHERE p.id != sqlc.arg(post_id)
ORDER BY e.embedding <=> (SELECT embedding FROM post_embeddings WHERE post_id = sqlc.arg(post_id))
LIMIT sqlc.arg(result_limit);

-- name: KeywordCandidates :many
-- KEYWORD arm of hybrid search: exact-term (full-text) matching, ranked by
-- ts_rank. Catches proper nouns / rare jargon (library names, error codes,
-- usernames) that the embedding model blurs away.
-- websearch_to_tsquery parses raw user input safely (quotes, OR, -negation) and
-- never throws on weird syntax. It uses the same to_tsvector expression as the
-- GIN index, so the WHERE clause is index-accelerated.
SELECT id, author_id, content, created_at
FROM posts
WHERE to_tsvector('english', content) @@ websearch_to_tsquery('english', sqlc.arg(query))
ORDER BY ts_rank(to_tsvector('english', content), websearch_to_tsquery('english', sqlc.arg(query))) DESC
LIMIT sqlc.arg(result_limit);

-- name: SemanticCandidates :many
-- SEMANTIC arm of hybrid search: PURE nearest-neighbour by meaning (cosine),
-- with NO recency/engagement re-rank. RRF fuses raw RANKS, so each arm must be
-- a clean ranked list — mixing quality signals in here would muddy the fusion.
-- HNSW-accelerated, same as the semantic /search path.
SELECT p.id, p.author_id, p.content, p.created_at
FROM posts p
JOIN post_embeddings e ON e.post_id = p.id
ORDER BY e.embedding <=> sqlc.arg(query_embedding)::vector
LIMIT sqlc.arg(result_limit);

-- name: FindNearDuplicates :many
-- Near-duplicate / astroturf detection. Returns every OTHER post whose embedding
-- is within a cosine DISTANCE THRESHOLD of the given post. Unlike GetRelatedPosts
-- (which returns the top-N nearest, always something), this uses an ABSOLUTE
-- threshold — the threshold itself is the definition of "is this a copy?". Posts
-- below it are near-identical in meaning even if the words were shuffled.
WITH target AS (
    SELECT embedding FROM post_embeddings WHERE post_id = sqlc.arg(post_id)
)
SELECT p.id, p.author_id, p.content,
       (e.embedding <=> (SELECT embedding FROM target))::float8 AS distance
FROM posts p
JOIN post_embeddings e ON e.post_id = p.id
WHERE p.id != sqlc.arg(post_id)
  AND (e.embedding <=> (SELECT embedding FROM target)) < sqlc.arg(max_distance)::float8
ORDER BY distance
LIMIT sqlc.arg(result_limit);
