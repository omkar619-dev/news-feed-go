package post

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omkar619-dev/news-feed-go/internal/auth"
	"github.com/omkar619-dev/news-feed-go/internal/repository/postgres/sqlc"
	"github.com/omkar619-dev/news-feed-go/internal/service"
)

// CreatePostRequest is the JSON body a client POSTs to /posts (only content;
// the author always comes from the token, never the client).
type CreatePostRequest struct {
	Content string `json:"content"`
}

// PostResponse is the HTTP wire shape for a post.
type PostResponse struct {
	ID        int64     `json:"id"`
	AuthorID  int64     `json:"author_id"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

// errorJSON writes a structured JSON error (shared across the post package).
func errorJSON(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// replayResponse writes a previously-stored idempotent response verbatim.
func replayResponse(w http.ResponseWriter, status int32, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Idempotency-Replayed", "true")
	w.WriteHeader(int(status))
	w.Write(body)
}

// NewCreatePostHandler: POST /posts. The HTTP adapter owns the transaction and the
// idempotency-key layer (an HTTP concern); the post + outbox event come from the
// shared service.CreatePost, which the TUI also calls (without idempotency).
func NewCreatePostHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := auth.UserIDFromContext(r.Context())
		if !ok {
			errorJSON(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		idemKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))

		// FAST-PATH replay: already completed this (user, key)? Return the stored
		// response, do nothing else. (Optimization; the real guard is the unique
		// constraint inside the tx below.)
		if idemKey != "" {
			prior, err := sqlc.New(pool).GetIdempotentResponse(r.Context(), sqlc.GetIdempotentResponseParams{
				UserID: userID,
				Key:    idemKey,
			})
			switch {
			case err == nil:
				replayResponse(w, prior.ResponseStatus, prior.ResponseBody)
				return
			case errors.Is(err, pgx.ErrNoRows):
				// first time we've seen this key — fall through
			default:
				errorJSON(w, http.StatusInternalServerError, "idempotency lookup failed")
				return
			}
		}

		var req CreatePostRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			errorJSON(w, http.StatusBadRequest, "invalid JSON body")
			return
		}

		// Begin the transaction the HTTP adapter owns.
		tx, err := pool.Begin(r.Context())
		if err != nil {
			errorJSON(w, http.StatusInternalServerError, "could not start transaction")
			return
		}
		defer tx.Rollback(r.Context()) // no-op once committed
		qtx := sqlc.New(tx)

		// DOMAIN: validate + insert post + write the outbox event, on this tx.
		// This exact call is what the TUI makes too.
		postRow, err := service.CreatePost(r.Context(), qtx, userID, req.Content)
		if err != nil {
			switch {
			case errors.Is(err, service.ErrEmptyContent):
				errorJSON(w, http.StatusBadRequest, "content is required")
			case errors.Is(err, service.ErrContentTooLong):
				errorJSON(w, http.StatusBadRequest, "content exceeds 280 characters")
			default:
				errorJSON(w, http.StatusInternalServerError, "could not create post")
			}
			return
		}

		// Marshal the response once — we send it and (if keyed) store it.
		respBody, err := json.Marshal(PostResponse{
			ID:        postRow.ID,
			AuthorID:  postRow.AuthorID,
			Content:   postRow.Content,
			CreatedAt: postRow.CreatedAt.Time,
		})
		if err != nil {
			errorJSON(w, http.StatusInternalServerError, "could not encode response")
			return
		}

		// HTTP-ONLY: record (user, key) -> response IN THIS SAME tx. A concurrent
		// duplicate that already committed this key hits 23505 → we roll back (post
		// undone too) and replay the winner. The TUI skips all of this.
		if idemKey != "" {
			if err := qtx.InsertIdempotencyKey(r.Context(), sqlc.InsertIdempotencyKeyParams{
				UserID:         userID,
				Key:            idemKey,
				ResponseStatus: http.StatusCreated,
				ResponseBody:   respBody,
			}); err != nil {
				var pgErr *pgconn.PgError
				if errors.As(err, &pgErr) && pgErr.Code == "23505" {
					_ = tx.Rollback(r.Context())
					prior, lookupErr := sqlc.New(pool).GetIdempotentResponse(r.Context(), sqlc.GetIdempotentResponseParams{
						UserID: userID,
						Key:    idemKey,
					})
					if lookupErr == nil {
						replayResponse(w, prior.ResponseStatus, prior.ResponseBody)
						return
					}
				}
				errorJSON(w, http.StatusInternalServerError, "could not record idempotency key")
				return
			}
		}

		if err := tx.Commit(r.Context()); err != nil {
			errorJSON(w, http.StatusInternalServerError, "could not commit")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write(respBody)
	}
}
