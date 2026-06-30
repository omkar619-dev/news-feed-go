package follow

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/omkar619-dev/news-feed-go/internal/auth"
	"github.com/omkar619-dev/news-feed-go/internal/repository/postgres/sqlc"
	"github.com/omkar619-dev/news-feed-go/internal/service"
)

// errorJSON writes a structured JSON error (same helper style as other pkgs).
func errorJSON(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// NewFollowHandler: the AUTHENTICATED user follows user {id}. Thin adapter over
// service.FollowUser — the follower is always the token's userID, never client input.
func NewFollowHandler(queries sqlc.Querier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		followerID, ok := auth.UserIDFromContext(r.Context())
		if !ok {
			errorJSON(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		followeeID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			errorJSON(w, http.StatusBadRequest, "invalid user id")
			return
		}

		if err := service.FollowUser(r.Context(), queries, followerID, followeeID); err != nil {
			switch {
			case errors.Is(err, service.ErrCannotFollowSelf):
				errorJSON(w, http.StatusBadRequest, "you cannot follow yourself")
			case errors.Is(err, service.ErrUserNotFound):
				errorJSON(w, http.StatusNotFound, "user not found")
			default:
				errorJSON(w, http.StatusInternalServerError, "could not follow user")
			}
			return
		}

		// 204 No Content. Idempotent: re-following also lands here.
		w.WriteHeader(http.StatusNoContent)
	}
}

// NewUnfollowHandler: the AUTHENTICATED user unfollows user {id}. Thin adapter.
func NewUnfollowHandler(queries sqlc.Querier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		followerID, ok := auth.UserIDFromContext(r.Context())
		if !ok {
			errorJSON(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		followeeID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			errorJSON(w, http.StatusBadRequest, "invalid user id")
			return
		}

		if err := service.UnfollowUser(r.Context(), queries, followerID, followeeID); err != nil {
			errorJSON(w, http.StatusInternalServerError, "could not unfollow user")
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}
