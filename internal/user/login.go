package user

import (
    "encoding/json"
    "net/http"
    "strings"

    "golang.org/x/crypto/bcrypt"

    "github.com/omkar619-dev/news-feed-go/internal/auth"
    "github.com/omkar619-dev/news-feed-go/internal/repository/postgres/sqlc"
)

// LoginRequest is the JSON body for /login.
type LoginRequest struct {
    Email    string `json:"email"`
    Password string `json:"password"`
}

// LoginResponse returns the JWT plus basic user info.
type LoginResponse struct {
    Token string `json:"token"`
    User  struct {
        ID       int64  `json:"id"`
        Username string `json:"username"`
        Email    string `json:"email"`
    } `json:"user"`
}

// A pre-computed bcrypt hash of a dummy password.
// Used to equalize response time when the user doesn't exist (timing-attack defense).
const dummyHash = "$2a$10$abcdefghijklmnopqrstuv.wxyz0123456789ABCDEFGHIJKLMNOPQR"

func NewLoginHandler(queries sqlc.Querier, tm *auth.TokenManager) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        if r.Method != http.MethodPost {
            errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
            return
        }

        var req LoginRequest
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            errorJSON(w, http.StatusBadRequest, "invalid JSON body")
            return
        }

        req.Email = strings.TrimSpace(strings.ToLower(req.Email))
        if req.Email == "" || req.Password == "" {
            errorJSON(w, http.StatusBadRequest, "email and password are required")
            return
        }

        // Look up the user by email
        user, err := queries.GetUserByEmail(r.Context(), req.Email)
        if err != nil {
            // User not found OR db error. To avoid leaking which emails exist,
            // do a dummy bcrypt comparison (equalize timing) then return generic 401.
            bcrypt.CompareHashAndPassword([]byte(dummyHash), []byte(req.Password))
            errorJSON(w, http.StatusUnauthorized, "invalid email or password")
            return
        }

        // Compare submitted password against the stored hash.
        if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
            errorJSON(w, http.StatusUnauthorized, "invalid email or password")
            return
        }

        // Credentials valid — issue a token.
        token, err := tm.Generate(user.ID)
        if err != nil {
            errorJSON(w, http.StatusInternalServerError, "could not generate token")
            return
        }

        // Build and send the response.
        var resp LoginResponse
        resp.Token = token
        resp.User.ID = user.ID
        resp.User.Username = user.Username
        resp.User.Email = user.Email

        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(http.StatusOK)
        json.NewEncoder(w).Encode(resp)
    }
}