// Package search exposes semantic search and "related posts" over pgvector.
package search

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/pgvector/pgvector-go"

	"github.com/omkar619-dev/news-feed-go/internal/embed"
	"github.com/omkar619-dev/news-feed-go/internal/post"
	"github.com/omkar619-dev/news-feed-go/internal/repository/postgres/sqlc"
)

const (
	defaultSearchLimit = 10
	maxSearchLimit     = 50
)

// SearchResponse is the list of matching posts.
type SearchResponse struct {
	Posts []post.PostResponse `json:"posts"`
}

func errorJSON(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// parseLimit reads ?limit= (default 10, hard cap 50).
func parseLimit(r *http.Request) (int32, bool) {
	limit := defaultSearchLimit
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			return 0, false
		}
		if n > maxSearchLimit {
			n = maxSearchLimit
		}
		limit = n
	}
	return int32(limit), true
}

// mapPosts turns DB rows into the public post shape (same idiom as elsewhere).
func mapPosts(rows []sqlc.Post) []post.PostResponse {
	out := make([]post.PostResponse, 0, len(rows))
	for _, p := range rows {
		out = append(out, post.PostResponse{
			ID:        p.ID,
			AuthorID:  p.AuthorID,
			Content:   p.Content,
			CreatedAt: p.CreatedAt.Time,
		})
	}
	return out
}

// NewSearchHandler: PUBLIC — GET /search?q=...  Semantic search by MEANING.
func NewSearchHandler(queries sqlc.Querier, embedder *embed.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		if q == "" {
			errorJSON(w, http.StatusBadRequest, "query parameter 'q' is required")
			return
		}
		limit, ok := parseLimit(r)
		if !ok {
			errorJSON(w, http.StatusBadRequest, "invalid limit")
			return
		}

		// 1. Turn the SEARCH TEXT into an embedding (same model as the posts).
		vec, err := embedder.Embed(r.Context(), q)
		if err != nil {
			errorJSON(w, http.StatusServiceUnavailable, "search temporarily unavailable (embedding failed)")
			return
		}

		// 2. Ask Postgres for the posts whose embedding is closest to it.
		rows, err := queries.SearchPostsByEmbedding(r.Context(), sqlc.SearchPostsByEmbeddingParams{
			QueryEmbedding: pgvector.NewVector(vec),
			ResultLimit:    limit,
		})
		if err != nil {
			errorJSON(w, http.StatusInternalServerError, "search failed")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(SearchResponse{Posts: mapPosts(rows)})
	}
}

// NewRelatedHandler: PUBLIC — GET /posts/{id}/related  "more like this".
// No embedder needed — it reuses the post's ALREADY-stored embedding (the query
// does the lookup via a subquery).
func NewRelatedHandler(queries sqlc.Querier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			errorJSON(w, http.StatusBadRequest, "invalid post id")
			return
		}
		limit, ok := parseLimit(r)
		if !ok {
			errorJSON(w, http.StatusBadRequest, "invalid limit")
			return
		}

		rows, err := queries.GetRelatedPosts(r.Context(), sqlc.GetRelatedPostsParams{
			PostID:      id,
			ResultLimit: limit,
		})
		if err != nil {
			errorJSON(w, http.StatusInternalServerError, "could not fetch related posts")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(SearchResponse{Posts: mapPosts(rows)})
	}
}
