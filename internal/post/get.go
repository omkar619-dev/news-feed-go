package post

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/omkar619-dev/news-feed-go/internal/repository/postgres/sqlc"
)

// NewGetPostHandler returns a handler that fetches a single post by its ID.
// This route is PUBLIC (mounted outside the auth group) — anyone can read a
// post. It reuses PostResponse and errorJSON from create.go (same package).
func NewGetPostHandler(queries sqlc.Querier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 1. Pull the {id} segment from the URL. It arrives as a STRING
		//    because a URL is just text — "/posts/5" gives us "5".
		idStr := chi.URLParam(r, "id")

		// 2. Convert "5" → int64(5). Base 10, 64-bit. If the segment isn't a
		//    number (e.g. /posts/abc), this fails and we 400 — no DB call.
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			errorJSON(w, http.StatusBadRequest, "invalid post id")
			return
		}

		// 3. Look it up. Three outcomes, three status codes:
		postRow, err := queries.GetPostByID(r.Context(), id)
		if err != nil {
			// pgx.ErrNoRows = the query ran fine but matched zero rows.
			// That's "not found" (404), NOT a server error.
			if errors.Is(err, pgx.ErrNoRows) {
				errorJSON(w, http.StatusNotFound, "post not found")
				return
			}
			// Any other error is a real DB/server problem.
			errorJSON(w, http.StatusInternalServerError, "could not fetch post")
			return
		}

		// 4. Found → 200 OK with the post.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(PostResponse{
			ID:        postRow.ID,
			AuthorID:  postRow.AuthorID,
			Content:   postRow.Content,
			CreatedAt: postRow.CreatedAt.Time,
		})
	}
}
