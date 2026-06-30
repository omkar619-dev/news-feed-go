package auth

import (
	"context"
	"net/http"
	"strings"
)

// contextKey is a private type used only for context keys in this package.
//
// Why a custom type instead of a plain string? Context keys are compared by
// BOTH value AND type. If we used the bare string "userID", some other package
// could also use "userID" and accidentally overwrite or read our value. By
// defining our own unexported type, our key is unique to this package — no
// other package can even construct it. This is the standard Go pattern.
type contextKey string

// userIDKey is the single key under which we store the authenticated user ID.
// It's unexported, so only code in this package can read/write the context slot.
const userIDKey contextKey = "userID"

// RequireAuth is middleware that guards protected routes.
//
// Shape: func(next http.Handler) http.Handler — it takes "the thing that runs
// after me" and returns a NEW handler that does auth work first. This exact
// signature is what chi's r.Use() expects too, so it'll drop in later unchanged.
//
// On success: extracts the user ID from a valid Bearer JWT, stamps it into the
// request context, and calls next. On failure: writes 401 and NEVER calls next
// (short-circuit — the protected handler never runs).
func (tm *TokenManager) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. Grab the Authorization header. No header → not logged in.
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, "missing authorization header", http.StatusUnauthorized)
			return
		}

		// 2. It must be exactly "Bearer <token>" — two space-separated parts.
		//    SplitN with limit 2 means the token itself can contain no extra
		//    surprises; EqualFold makes the scheme check case-insensitive
		//    ("bearer", "Bearer", "BEARER" all accepted).
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			http.Error(w, "malformed authorization header", http.StatusUnauthorized)
			return
		}
		tokenString := parts[1]

		// 3. Validate: checks the HMAC signature, the expiry, and pulls the
		//    user ID out of the "sub" claim. Any failure → generic 401 (we
		//    don't leak WHY it failed, just that it did).
		userID, err := tm.Validate(tokenString)
		if err != nil {
			http.Error(w, "invalid or expired token", http.StatusUnauthorized)
			return
		}

		// 4. Stamp the hand: attach userID to a COPY of the request's context.
		//    context.WithValue returns a new ctx; it never mutates the old one.
		ctx := context.WithValue(r.Context(), userIDKey, userID)

		// 5. Let the request through — but with the enriched context.
		//    r.WithContext(ctx) returns a shallow copy of r carrying the new ctx.
		//    Downstream handlers now read userID via UserIDFromContext.
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// UserIDFromContext reads the authenticated user ID that RequireAuth stored.
//
// The comma-ok return tells the caller whether the value was actually present
// AND of type int64. A handler behind RequireAuth can trust ok==true, but any
// handler NOT behind the middleware would get ok==false instead of a panic.
func UserIDFromContext(ctx context.Context) (int64, bool) {
	userID, ok := ctx.Value(userIDKey).(int64)
	return userID, ok
}
