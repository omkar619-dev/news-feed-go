package post

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/omkar619-dev/news-feed-go/internal/auth"
	"github.com/omkar619-dev/news-feed-go/internal/repository/postgres/sqlc"
	"github.com/omkar619-dev/news-feed-go/internal/service"
)

// NewDeletePostHandler: DELETE /posts/{id} — thin adapter over service.DeletePost,
// which enforces ownership. The adapter just maps the domain errors to statuses:
// "no such post" → 404, "not yours" → 403.
func NewDeletePostHandler(queries sqlc.Querier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := auth.UserIDFromContext(r.Context())
		if !ok {
			errorJSON(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			errorJSON(w, http.StatusBadRequest, "invalid post id")
			return
		}

		if err := service.DeletePost(r.Context(), queries, userID, id); err != nil {
			switch {
			case errors.Is(err, service.ErrPostNotFound):
				errorJSON(w, http.StatusNotFound, "post not found")
			case errors.Is(err, service.ErrNotPostOwner):
				errorJSON(w, http.StatusForbidden, "you can only delete your own posts")
			default:
				errorJSON(w, http.StatusInternalServerError, "could not delete post")
			}
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}
