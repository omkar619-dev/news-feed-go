package user

import (
    "encoding/json"
    "errors"
    "net/http"
    "strings"

    "github.com/jackc/pgx/v5"
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
        // Only POST is allowed
        if r.Method != http.MethodPost {
            errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
            return
        }

        // Parse the JSON body
        var req SignupRequest
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            errorJSON(w, http.StatusBadRequest, "invalid JSON body")
            return
        }

        // Normalize email (lowercase, trim whitespace)
        req.Email = strings.TrimSpace(strings.ToLower(req.Email))
        req.Username = strings.TrimSpace(req.Username)

        // Validate required fields
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

        // Check if email already exists
        _, err := queries.GetUserByEmail(r.Context(), req.Email)
        if err == nil {
            errorJSON(w, http.StatusConflict, "email already in use")
            return
        }
        if !errors.Is(err, pgx.ErrNoRows) {
            // Some unexpected DB error — not "user doesn't exist"
            errorJSON(w, http.StatusInternalServerError, "database error")
            return
        }

        // Hash the password
        hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
        if err != nil {
            errorJSON(w, http.StatusInternalServerError, "could not hash password")
            return
        }

        // Create the user
        userRow, err := queries.CreateUser(r.Context(), sqlc.CreateUserParams{
            Username:     req.Username,
            Email:        req.Email,
            PasswordHash: string(hash),
        })
        if err != nil {
            errorJSON(w, http.StatusInternalServerError, "could not create user")
            return
        }

        // Return the created user as JSON
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(http.StatusCreated)
        json.NewEncoder(w).Encode(SignupResponse{
            ID:       userRow.ID,
            Username: userRow.Username,
            Email:    userRow.Email,
        })
    }
}