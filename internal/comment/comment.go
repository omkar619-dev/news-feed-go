package comment

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/omkar619-dev/news-feed-go/internal/auth"
	"github.com/omkar619-dev/news-feed-go/internal/repository/postgres/sqlc"
	"github.com/omkar619-dev/news-feed-go/internal/service"
)

func errorJSON(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// CreateCommentRequest: parent_id is OPTIONAL — omit for a top-level comment,
// include another comment's id to make this a reply. (*int64 so "absent" = nil.)
type CreateCommentRequest struct {
	Content  string `json:"content"`
	ParentID *int64 `json:"parent_id"`
}

// CommentResponse is one comment on the wire. parent_id is null for top-level;
// depth only appears in the tree response.
type CommentResponse struct {
	ID        int64     `json:"id"`
	PostID    int64     `json:"post_id"`
	AuthorID  int64     `json:"author_id"`
	ParentID  *int64    `json:"parent_id"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
	Depth     int32     `json:"depth,omitempty"`
}

// nullableID maps the DB's nullable pgtype.Int8 to a clean JSON *int64 (the
// reverse of the service's toPgInt8). This stays here — it's WIRE formatting.
func nullableID(v pgtype.Int8) *int64 {
	if !v.Valid {
		return nil
	}
	id := v.Int64
	return &id
}

// NewCreateCommentHandler: POST /posts/{id}/comments — thin adapter over service.AddComment.
func NewCreateCommentHandler(queries sqlc.Querier) http.HandlerFunc {
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
		var req CreateCommentRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			errorJSON(w, http.StatusBadRequest, "invalid JSON body")
			return
		}

		c, err := service.AddComment(r.Context(), queries, postID, userID, req.ParentID, req.Content)
		if err != nil {
			switch {
			case errors.Is(err, service.ErrEmptyContent):
				errorJSON(w, http.StatusBadRequest, "content is required")
			case errors.Is(err, service.ErrCommentTargetMissing):
				errorJSON(w, http.StatusNotFound, "post or parent comment not found")
			default:
				errorJSON(w, http.StatusInternalServerError, "could not create comment")
			}
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(CommentResponse{
			ID:        c.ID,
			PostID:    c.PostID,
			AuthorID:  c.AuthorID,
			ParentID:  nullableID(c.ParentID),
			Content:   c.Content,
			CreatedAt: c.CreatedAt.Time,
		})
	}
}

// NewGetCommentsHandler: GET /posts/{id}/comments — PUBLIC; the threaded tree.
func NewGetCommentsHandler(queries sqlc.Querier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		postID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			errorJSON(w, http.StatusBadRequest, "invalid post id")
			return
		}

		rows, err := service.CommentTree(r.Context(), queries, postID)
		if err != nil {
			errorJSON(w, http.StatusInternalServerError, "could not load comments")
			return
		}

		out := make([]CommentResponse, 0, len(rows))
		for _, c := range rows {
			out = append(out, CommentResponse{
				ID:        c.ID,
				PostID:    c.PostID,
				AuthorID:  c.AuthorID,
				ParentID:  nullableID(c.ParentID),
				Content:   c.Content,
				CreatedAt: c.CreatedAt.Time,
				Depth:     c.Depth,
			})
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{"comments": out})
	}
}

// NewDeleteCommentHandler: DELETE /comments/{id} — owner-only (cascade removes
// the reply subtree). Thin adapter over service.DeleteComment.
func NewDeleteCommentHandler(queries sqlc.Querier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := auth.UserIDFromContext(r.Context())
		if !ok {
			errorJSON(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		commentID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			errorJSON(w, http.StatusBadRequest, "invalid comment id")
			return
		}

		if err := service.DeleteComment(r.Context(), queries, userID, commentID); err != nil {
			if errors.Is(err, service.ErrCommentNotFound) {
				errorJSON(w, http.StatusNotFound, "comment not found")
				return
			}
			errorJSON(w, http.StatusInternalServerError, "could not delete comment")
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}
