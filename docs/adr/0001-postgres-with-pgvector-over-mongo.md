# ADR 0001 — Postgres + pgvector over MongoDB

**Status**: Accepted
**Date**: 2026-05-28

## Context

The feed needs two query patterns:
1. Relational queries (user follows, post threads, comment trees)
2. Vector similarity search (related posts via content embeddings)

Options considered:
- PostgreSQL + pgvector extension
- MongoDB + Atlas Vector Search
- PostgreSQL + Pinecone (separate vector store)

## Decision

Use **PostgreSQL with pgvector** as the single store for both relational and vector data.

## Rationale

- One database to operate (vs Postgres + Pinecone)
- ACID guarantees for follow/post operations
- pgvector handles up to ~5M vectors with HNSW index at sub-100ms p99 — well above this project's scale
- Familiar query language (SQL) reduces operational surprises
- Free, no vendor lock-in

## Consequences

- Cannot natively shard across many vector DBs (acceptable at this scale)
- Vector index rebuilds during heavy write traffic may impact query latency
- Will need to monitor pgvector performance carefully when post count exceeds 1M

## Migration path if outgrown

Extract vectors into qdrant or Pinecone as a separate service; keep relational data in Postgres. Application code already abstracts the vector layer behind `internal/embedding/` interfaces.