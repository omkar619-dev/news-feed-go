package post

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/omkar619-dev/news-feed-go/internal/auth"
	"github.com/omkar619-dev/news-feed-go/internal/cache"
	"github.com/omkar619-dev/news-feed-go/internal/repository/postgres/sqlc"
	"github.com/omkar619-dev/news-feed-go/internal/service"
)

// LikeResponse is the HTTP wire shape for a like/unlike result.
type LikeResponse struct {
	Liked     bool  `json:"liked"`
	LikeCount int64 `json:"like_count"`
}

// NewLikeHandler: POST /posts/{id}/like — a thin adapter over service.Like.
// Its only jobs are HTTP ones: pull identity from the token, parse the path,
// call the domain function, and translate the result/error into HTTP.
func NewLikeHandler(queries sqlc.Querier, counter *cache.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := auth.UserIDFromContext(r.Context())
		if !ok {
			errorJSON(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		postID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			errorJSON(w, http.StatusBadRequest, "invalid post id")
			return
		}

		res, err := service.Like(r.Context(), queries, counter, userID, postID)
		if err != nil {
			if errors.Is(err, service.ErrPostNotFound) {
				errorJSON(w, http.StatusNotFound, "post not found")
				return
			}
			errorJSON(w, http.StatusInternalServerError, "could not like post")
			return
		}
		writeJSON(w, LikeResponse{Liked: res.Liked, LikeCount: res.Count})
	}
}

// NewUnlikeHandler: DELETE /posts/{id}/like — a thin adapter over service.Unlike.
func NewUnlikeHandler(queries sqlc.Querier, counter *cache.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := auth.UserIDFromContext(r.Context())
		if !ok {
			errorJSON(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		postID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			errorJSON(w, http.StatusBadRequest, "invalid post id")
			return
		}

		res, err := service.Unlike(r.Context(), queries, counter, userID, postID)
		if err != nil {
			errorJSON(w, http.StatusInternalServerError, "could not unlike post")
			return
		}
		writeJSON(w, LikeResponse{Liked: res.Liked, LikeCount: res.Count})
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(v)
}
