package follow

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/omkar619-dev/news-feed-go/internal/repository/postgres/sqlc"
)

// UserSummary is the PUBLIC view of a user — id + username only. Deliberately
// NO email or other PII, because these endpoints are public.
type UserSummary struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

// UserListResponse wraps a list of user summaries.
type UserListResponse struct {
	Users []UserSummary `json:"users"`
}

// NewListFollowersHandler: PUBLIC — lists the users who follow {id}.
func NewListFollowersHandler(queries sqlc.Querier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			errorJSON(w, http.StatusBadRequest, "invalid user id")
			return
		}

		rows, err := queries.ListFollowers(r.Context(), id)
		if err != nil {
			errorJSON(w, http.StatusInternalServerError, "could not list followers")
			return
		}

		// Map the sqlc row type → our public summary type.
		users := make([]UserSummary, 0, len(rows))
		for _, row := range rows {
			users = append(users, UserSummary{ID: row.ID, Username: row.Username})
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(UserListResponse{Users: users})
	}
}

// NewListFollowingHandler: PUBLIC — lists the users that {id} follows.
func NewListFollowingHandler(queries sqlc.Querier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			errorJSON(w, http.StatusBadRequest, "invalid user id")
			return
		}

		rows, err := queries.ListFollowing(r.Context(), id)
		if err != nil {
			errorJSON(w, http.StatusInternalServerError, "could not list following")
			return
		}

		users := make([]UserSummary, 0, len(rows))
		for _, row := range rows {
			users = append(users, UserSummary{ID: row.ID, Username: row.Username})
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(UserListResponse{Users: users})
	}
}
