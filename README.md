# news-feed-go

A Twitter/Reddit-style news feed service in Go. Built for production-shape distributed-systems learning + portfolio.

## What it does (planned scope)

- User signup, login, JWT auth
- Post creation (text + optional image)
- Follow/unfollow users
- Personalized timeline (posts from followed users, ranked)
- Related posts via semantic search (pgvector embeddings)
- Real-time notifications via WebSockets (SSE fallback)
- LLM-powered content moderation

## Architecture

- **Language**: Go 1.25
- **Database**: PostgreSQL 16 (primary + read replica) with pgvector extension
- **Cache**: Redis 7 (cache-aside for hot timelines + rate limiting)
- **Queue**: RabbitMQ (fan-out-on-write for feed distribution)
- **Search**: BM25 (Postgres FTS) + dense vectors (pgvector) + RRF fusion
- **Deploy**: Helm chart on Kubernetes (k3s)
- **CI/CD**: GitHub Actions

## Status

Active development. See [docs/](./docs) for architecture decisions and deep-dives.

## Why

Production-shape side project for the [companion blog](https://omkar-blog.pages.dev). Each design decision documented as an ADR.