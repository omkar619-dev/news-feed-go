package feed

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/omkar619-dev/news-feed-go/internal/auth"
	"github.com/omkar619-dev/news-feed-go/internal/cache"
	"github.com/omkar619-dev/news-feed-go/internal/cursor"
	"github.com/omkar619-dev/news-feed-go/internal/post"
	"github.com/omkar619-dev/news-feed-go/internal/repository/postgres/sqlc"
)

const (
	defaultFeedLimit = 20
	maxFeedLimit     = 50
	feedCacheTTL     = 30 * time.Second // how long a cached first page stays fresh
)

// FeedResponse is one page of the user's home feed + the next-page cursor.
type FeedResponse struct {
	Posts      []post.PostResponse `json:"posts"`
	NextCursor string              `json:"next_cursor"`
}

func errorJSON(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// NewFeedHandler: PROTECTED, cursor-paginated, with cache-aside on the first page.
func NewFeedHandler(queries sqlc.Querier, cacheClient *cache.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := auth.UserIDFromContext(r.Context())
		if !ok {
			errorJSON(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		// Page size from ?limit= (optional). Default 20, hard cap 50.
		limit := defaultFeedLimit
		if s := r.URL.Query().Get("limit"); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 {
				errorJSON(w, http.StatusBadRequest, "invalid limit")
				return
			}
			if n > maxFeedLimit {
				n = maxFeedLimit
			}
			limit = n
		}
		fetch := int32(limit + 1) // peek row, to detect a next page
		cur := r.URL.Query().Get("cursor")

		// We only cache the canonical FIRST page (no cursor, default limit) —
		// that's the hot path. Other pages bypass the cache.
		cacheable := cur == "" && limit == defaultFeedLimit
		cacheKey := fmt.Sprintf("feed:%d", userID)

		// ── Cache lookup (HIT serves straight from Redis) ──
		if cacheable {
			if cached, found, err := cacheClient.Get(r.Context(), cacheKey); err == nil && found {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Cache", "HIT")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(cached))
				return
			}
			// On a Redis error we just fall through to Postgres — the cache is
			// best-effort and must never take the feed down.
		}

		// ── MISS (or non-cacheable): query Postgres ──
		var (
			rows []sqlc.Post
			err  error
		)
		if cur == "" {
			rows, err = queries.GetFeedFirst(r.Context(), sqlc.GetFeedFirstParams{
				UserID:    userID,
				PageLimit: fetch,
			})
		} else {
			t, id, derr := cursor.Decode(cur)
			if derr != nil {
				errorJSON(w, http.StatusBadRequest, "invalid cursor")
				return
			}
			rows, err = queries.GetFeedAfter(r.Context(), sqlc.GetFeedAfterParams{
				UserID:          userID,
				CursorCreatedAt: pgtype.Timestamptz{Time: t, Valid: true},
				CursorID:        id,
				PageLimit:       fetch,
			})
		}
		if err != nil {
			errorJSON(w, http.StatusInternalServerError, "could not load feed")
			return
		}

		hasMore := len(rows) > limit
		if hasMore {
			rows = rows[:limit]
		}

		posts := make([]post.PostResponse, 0, len(rows))
		for _, p := range rows {
			posts = append(posts, post.PostResponse{
				ID:        p.ID,
				AuthorID:  p.AuthorID,
				Content:   p.Content,
				CreatedAt: p.CreatedAt.Time,
			})
		}

		next := ""
		if hasMore && len(rows) > 0 {
			last := rows[len(rows)-1]
			next = cursor.Encode(last.CreatedAt.Time, last.ID)
		}

		// Marshal ONCE, so we cache and send the exact same bytes.
		body, err := json.Marshal(FeedResponse{Posts: posts, NextCursor: next})
		if err != nil {
			errorJSON(w, http.StatusInternalServerError, "could not encode feed")
			return
		}

		// Populate the cache on a miss (best-effort — ignore cache errors).
		if cacheable {
			_ = cacheClient.Set(r.Context(), cacheKey, string(body), feedCacheTTL)
			w.Header().Set("X-Cache", "MISS")
		} else {
			w.Header().Set("X-Cache", "BYPASS")
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	}
}
