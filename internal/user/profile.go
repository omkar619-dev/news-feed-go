package user

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/omkar619-dev/news-feed-go/internal/repository/postgres/sqlc"
)

// ProfileResponse is a user's PUBLIC profile: basic info + live counts.
// No email here — that's PII, and this endpoint is public.
type ProfileResponse struct {
	ID             int64     `json:"id"`
	Username       string    `json:"username"`
	CreatedAt      time.Time `json:"created_at"`
	FollowersCount int64     `json:"followers_count"`
	FollowingCount int64     `json:"following_count"`
	PostsCount     int64     `json:"posts_count"`
}

// NewProfileHandler: PUBLIC — fetch a user's profile by username.
func NewProfileHandler(queries sqlc.Querier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Username comes from the path — it's already a string, no parsing.
		username := chi.URLParam(r, "username")

		profile, err := queries.GetProfileByUsername(r.Context(), username)
		if err != nil {
			// No such username → 404 (same ErrNoRows → 404 pattern as GetPostByID).
			if errors.Is(err, pgx.ErrNoRows) {
				errorJSON(w, http.StatusNotFound, "user not found")
				return
			}
			errorJSON(w, http.StatusInternalServerError, "could not fetch profile")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(ProfileResponse{
			ID:             profile.ID,
			Username:       profile.Username,
			CreatedAt:      profile.CreatedAt.Time,
			FollowersCount: profile.FollowersCount,
			FollowingCount: profile.FollowingCount,
			PostsCount:     profile.PostsCount,
		})
	}
}
