package user

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"

	"github.com/omkar619-dev/news-feed-go/internal/repository/postgres/sqlc"
)

// SignupRequest is the JSON body shape clients POST to /signup.
type SignupRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

// SignupResponse is what we send back on success.
// Note: NO password field. We never echo back or expose hashes.
type SignupResponse struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
}

// errorJSON writes a structured JSON error response.
func errorJSON(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// NewSignupHandler returns an HTTP handler that creates new users.
// It accepts a Querier (interface) so tests can pass mocks.
func NewSignupHandler(queries sqlc.Querier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Only POST is allowed.
		if r.Method != http.MethodPost {
			errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		// Parse the JSON body.
		var req SignupRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			errorJSON(w, http.StatusBadRequest, "invalid JSON body")
			return
		}

		// Normalize email (lowercase, trim) and username (trim).
		req.Email = strings.TrimSpace(strings.ToLower(req.Email))
		req.Username = strings.TrimSpace(req.Username)

		// Validate required fields.
		if req.Username == "" || req.Email == "" || req.Password == "" {
			errorJSON(w, http.StatusBadRequest, "username, email, and password are required")
			return
		}
		if len(req.Password) < 8 {
			errorJSON(w, http.StatusBadRequest, "password must be at least 8 characters")
			return
		}
		if !strings.Contains(req.Email, "@") {
			errorJSON(w, http.StatusBadRequest, "invalid email format")
			return
		}

		// Hash the password (bcrypt: one-way, salted, slow-by-design).
		hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		if err != nil {
			errorJSON(w, http.StatusInternalServerError, "could not hash password")
			return
		}

		// Insert the user. We deliberately do NOT pre-check for duplicates:
		// the DB's UNIQUE constraints on username/email are the real source of
		// truth. A SELECT-then-INSERT has a race (two concurrent signups can
		// both pass the check, then collide at INSERT). So we just INSERT and
		// inspect the error.
		userRow, err := queries.CreateUser(r.Context(), sqlc.CreateUserParams{
			Username:     req.Username,
			Email:        req.Email,
			PasswordHash: string(hash),
		})
		if err != nil {
			// Postgres signals a duplicate with SQLSTATE 23505 (unique_violation).
			// errors.As walks the wrapped error chain looking for a *pgconn.PgError.
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				// ConstraintName tells us WHICH unique index was violated
				// (Postgres names them users_email_key / users_username_key).
				switch {
				case strings.Contains(pgErr.ConstraintName, "email"):
					errorJSON(w, http.StatusConflict, "email already in use")
				case strings.Contains(pgErr.ConstraintName, "username"):
					errorJSON(w, http.StatusConflict, "username already taken")
				default:
					errorJSON(w, http.StatusConflict, "username or email already in use")
				}
				return
			}
			// Anything else is a genuine server-side failure.
			errorJSON(w, http.StatusInternalServerError, "could not create user")
			return
		}

		// Return the created user as JSON (201 Created).
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(SignupResponse{
			ID:       userRow.ID,
			Username: userRow.Username,
			Email:    userRow.Email,
		})
	}
}
