-- Embedding-gap demo: ONE post intentionally LEFT UN-EMBEDDED, to model the real
-- window between a post being created and the worker computing its embedding
-- (worker backlog, Ollama down, a just-posted item). Pure semantic search is
-- BLIND to it — there's no row in post_embeddings, so the SemanticCandidates
-- JOIN drops it. The keyword arm still finds it, so hybrid does too.
--
-- IMPORTANT: run this AFTER the backfill, and do NOT run the backfill again —
-- backfill embeds EVERY post, which would give 1029 an embedding and erase the
-- gap (and the lift along with it).
--
--   Get-Content eval\seed-gap.sql -Raw | docker exec -i news-feed-go-postgres-1 psql -U newsfeed -d newsfeed

INSERT INTO posts (id, author_id, content) VALUES
  (1029, 7, 'Migrating our auth service from JWT to PASETO tokens for safer defaults')
ON CONFLICT (id) DO NOTHING;

SELECT setval(pg_get_serial_sequence('posts', 'id'), (SELECT MAX(id) FROM posts));
