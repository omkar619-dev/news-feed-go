-- name: CreateUser :one
INSERT INTO users (
    username,
    email,
    password_hash
)
VALUES ($1, $2, $3)
RETURNING id, username, email, created_at;

-- name: GetUserByEmail :one
SELECT id, username, email, password_hash, created_at
FROM users
WHERE email = $1
LIMIT 1;

-- name: GetUserByID :one
SELECT id, username, email, created_at
FROM users
WHERE id = $1
LIMIT 1;

-- name: GetUserByUsername :one
SELECT id, username, email, created_at
FROM users
WHERE username = $1
LIMIT 1;

-- name: DeleteUser :exec
DELETE FROM users WHERE id = $1;