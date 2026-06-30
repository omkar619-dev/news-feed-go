-- Users
CREATE TABLE users (
    id BIGSERIAL PRIMARY KEY,
    username TEXT UNIQUE NOT NULL,
    email TEXT UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    is_celebrity BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Posts
CREATE TABLE posts (
    id BIGSERIAL PRIMARY KEY,
    author_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    content TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_posts_author_created ON posts(author_id, created_at DESC);
-- Full-text (keyword) search index — the keyword arm of hybrid search.
-- GIN index on the to_tsvector EXPRESSION; the FTS query must use the SAME
-- expression (to_tsvector('english', content)) for the planner to use it.
CREATE INDEX idx_posts_fts ON posts USING GIN (to_tsvector('english', content));

-- Follows
CREATE TABLE follows (
    follower_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    followee_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (follower_id, followee_id)
);
CREATE INDEX idx_follows_follower ON follows(follower_id);

-- Likes (a user likes a post; composite PK = like-at-most-once)
CREATE TABLE likes (
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    post_id BIGINT NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, post_id)
);
CREATE INDEX idx_likes_post ON likes(post_id);

-- Comments (threaded: parent_id self-references comments; NULL = top-level)
CREATE TABLE comments (
    id         BIGSERIAL PRIMARY KEY,
    post_id    BIGINT NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    author_id  BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    parent_id  BIGINT REFERENCES comments(id) ON DELETE CASCADE, -- NULL = top-level
    content    TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_comments_post ON comments(post_id);
CREATE INDEX idx_comments_parent ON comments(parent_id);

-- Timelines (precomputed feed entries from fan-out-on-write)
CREATE TABLE timelines (
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    post_id BIGINT NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    inserted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, post_id)
);
CREATE INDEX idx_timelines_user_inserted ON timelines(user_id, inserted_at DESC);

-- Post embeddings (pgvector for related-posts feature)
CREATE EXTENSION IF NOT EXISTS vector;
CREATE TABLE post_embeddings (
    post_id BIGINT PRIMARY KEY REFERENCES posts(id) ON DELETE CASCADE,
    embedding vector(384)
);
CREATE INDEX idx_post_embeddings_hnsw ON post_embeddings
    USING hnsw (embedding vector_cosine_ops);

-- Transactional outbox: domain events are inserted here in the SAME transaction
-- as the state change that produced them (see internal/post/create.go); the relay
-- (cmd/relay) then publishes them to RabbitMQ and stamps published_at. This is
-- what guarantees an event is never lost on a crash between the DB commit and the
-- broker publish (the dual-write problem).
CREATE TABLE outbox (
    id           BIGSERIAL PRIMARY KEY,
    event_type   TEXT NOT NULL,
    payload      JSONB NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    published_at TIMESTAMPTZ
);
-- Partial index: only UNPUBLISHED rows are indexed (published rows fall out of
-- it), so the relay's "find pending events" poll stays fast as the table grows.
CREATE INDEX idx_outbox_unpublished ON outbox (id) WHERE published_at IS NULL;

-- Idempotency keys: a client sends an Idempotency-Key header with POST /posts so
-- that retrying a request whose response was lost returns the ORIGINAL result
-- instead of creating a duplicate post. We store the response per (user, key).
-- The composite PRIMARY KEY doubles as the uniqueness guard that makes concurrent
-- duplicates impossible: the second transaction hits a unique violation and rolls
-- back — and because the post is inserted in that same transaction, no duplicate
-- post is ever created.
CREATE TABLE idempotency_keys (
    user_id         BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key             TEXT NOT NULL,
    response_status INT NOT NULL,
    response_body   JSONB NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, key)
);

-- SSH terminal access: maps an SSH public key to a user, so the Charm/wish TUI
-- (cmd/ssh) can identify who connected by their key ALONE — no password, no token.
-- The key is stored as "ssh-ed25519 AAAA..." (type + base64, comment stripped).
CREATE TABLE ssh_keys (
    public_key TEXT PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);