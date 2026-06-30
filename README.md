# news-feed-go

[![CI](https://github.com/omkar619-dev/news-feed-go/actions/workflows/ci.yml/badge.svg)](https://github.com/omkar619-dev/news-feed-go/actions/workflows/ci.yml)

A Twitter/Reddit-style news feed in Go — built production-shape for distributed-systems learning and as a portfolio piece. It serves **one feed through two front doors** — a REST/JSON API *and* an **SSH terminal app** — sitting on a single shared domain core, backed by Postgres + pgvector, RabbitMQ, Redis, and Ollama embeddings.

📝 Each design decision is written up on the [companion blog](https://omkar-blog.pages.dev) — including [the SSH front door & the ports-and-adapters refactor](https://omkar-blog.pages.dev/projects/a-social-feed-you-ssh-into/).

## What it does

- User signup / login / JWT auth
- Posts — create, read, delete, list-by-user (280-char)
- Follow / unfollow + a queryable social graph
- Home timeline via **fan-out-on-write** (a RabbitMQ worker)
- Likes with a **Redis hot-counter** (cold-seeded from the table)
- **Threaded comments** — self-referencing parent, rendered from a recursive CTE
- **Semantic + hybrid search** — Postgres full-text + pgvector dense vectors, fused with Reciprocal Rank Fusion
- **Near-duplicate detection** — cosine-distance threshold over the embeddings
- **Transactional outbox** → relay → broker (no dual-write bug)
- **SSH TUI** (Charm `wish` + Bubble Tea) — browse / post / like / comment / reply / follow, with your SSH key as the login
- Prometheus metrics + structured (JSON) logging

## Architecture

One Go image, **four roles**, each chosen at container start by which binary its Deployment runs:

| role (`cmd/…`) | job |
|---|---|
| `api`    | HTTP/JSON server (chi) — `:3000` |
| `ssh`    | SSH terminal app (`wish` + Bubble Tea) — `:23234` |
| `worker` | consumes `post.created` → fan-out + Ollama embeddings |
| `relay`  | drains the outbox table → RabbitMQ |

- **DB**: PostgreSQL 16 + pgvector
- **Cache**: Redis 7 — cache-aside timelines, the like hot-counter, rate limiting
- **Queue**: RabbitMQ — fan-out-on-write
- **Search**: Postgres full-text + pgvector + RRF fusion
- **Embeddings**: Ollama (`all-minilm`)
- **Deploy**: Helm chart on **k3s** — currently running on a 2008 Core-2-Duo homelab box, reachable over Tailscale ([the story](https://omkar-blog.pages.dev/projects/a-social-feed-you-ssh-into/))

## Run it locally

Needs **Go 1.25+**, **Docker**, and **[Ollama](https://ollama.com)**.

```bash
# 1. clone
git clone https://github.com/omkar619-dev/news-feed-go && cd news-feed-go

# 2. infra — Postgres (pgvector) :5432, RabbitMQ :5673, Redis :6380
docker compose up -d

# 3. load the schema
psql "postgres://newsfeed:newsfeed_dev@localhost:5432/newsfeed?sslmode=disable" \
  -f internal/repository/postgres/schema.sql

# 4. pull the embedding model (Ollama serves on :11434 by default)
ollama pull all-minilm

# 5. env — these defaults match docker-compose
export DATABASE_URL="postgres://newsfeed:newsfeed_dev@localhost:5432/newsfeed?sslmode=disable"
export REDIS_ADDR="localhost:6380"
export RABBITMQ_URL="amqp://newsfeed:newsfeed_dev@localhost:5673/"
export OLLAMA_URL="http://localhost:11434"
export JWT_SECRET="dev-secret"

# 6. run the four roles (each in its own terminal)
go run ./cmd/api      # REST API → :3000
go run ./cmd/worker   # fan-out + embeddings
go run ./cmd/relay    # outbox → RabbitMQ
go run ./cmd/ssh      # SSH TUI → :23234
```

### Use the REST API

```bash
# sign up, then log in (login is by EMAIL) to get a JWT
curl -s localhost:3000/signup -d '{"username":"ada","email":"ada@example.com","password":"secret123"}'
TOKEN=$(curl -s localhost:3000/login -d '{"email":"ada@example.com","password":"secret123"}' | jq -r .token)

# create a post (auth required, rate-limited to 5/min)
curl -s localhost:3000/posts -H "Authorization: Bearer $TOKEN" -d '{"content":"hello, distributed world"}'

# search — public. semantic, and hybrid (keyword + vector, RRF-fused)
curl -s "localhost:3000/search?q=distributed+systems"
curl -s "localhost:3000/search/hybrid?q=raft+consensus"
```

### Use the SSH TUI

Your SSH key *is* your login. Sign up first (above) to get a user id, then map your public key to it (`1` = your id from signup):

```bash
psql "postgres://newsfeed:newsfeed_dev@localhost:5432/newsfeed?sslmode=disable" \
  -c "INSERT INTO ssh_keys (public_key, user_id) VALUES ('$(cut -d' ' -f1,2 ~/.ssh/id_ed25519.pub)', 1);"

ssh -p 23234 localhost
```

Keys: `↑/↓` move · `enter` open thread · `n` post · `c` comment · `r` reply · `l`/`u` like/unlike · `f` follow · `d` delete · `q` quit. An **unregistered key connects as a read-only guest**.

## Why

A production-shape side project, with every design decision written up as an essay on the [companion blog](https://omkar-blog.pages.dev).
