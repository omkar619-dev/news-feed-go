package auth

import (
    "fmt"
    "strconv"
    "time"

    "github.com/golang-jwt/jwt/v5"
)

// TokenManager issues and validates JWTs.
// Holds the signing secret and token lifetime.
type TokenManager struct {
    secret []byte
    expiry time.Duration
}

// NewTokenManager constructs a TokenManager.
func NewTokenManager(secret string, expiry time.Duration) *TokenManager {
    return &TokenManager{
        secret: []byte(secret),
        expiry: expiry,
    }
}

// Generate creates a signed JWT for the given user ID.
func (tm *TokenManager) Generate(userID int64) (string, error) {
    now := time.Now()

    claims := jwt.RegisteredClaims{
        Subject:   strconv.FormatInt(userID, 10),
        IssuedAt:  jwt.NewNumericDate(now),
        ExpiresAt: jwt.NewNumericDate(now.Add(tm.expiry)),
    }

    token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
    signed, err := token.SignedString(tm.secret)
    if err != nil {
        return "", fmt.Errorf("sign token: %w", err)
    }
    return signed, nil
}

// Validate parses and verifies a JWT, returning the user ID it represents.
func (tm *TokenManager) Validate(tokenString string) (int64, error) {
    claims := &jwt.RegisteredClaims{}

    token, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (interface{}, error) {
        // Reject any token NOT signed with HMAC — prevents algorithm-confusion attacks.
        if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
            return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
        }
        return tm.secret, nil
    })
    if err != nil {
        return 0, fmt.Errorf("parse token: %w", err)
    }

    if !token.Valid {
        return 0, fmt.Errorf("invalid token")
    }

    userID, err := strconv.ParseInt(claims.Subject, 10, 64)
    if err != nil {
        return 0, fmt.Errorf("invalid subject claim: %w", err)
    }
    return userID, nil
}