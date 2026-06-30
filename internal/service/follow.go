package service

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/omkar619-dev/news-feed-go/internal/repository/postgres/sqlc"
)

// Follow-domain sentinel errors.
var (
	ErrCannotFollowSelf = errors.New("cannot follow yourself")
	ErrUserNotFound     = errors.New("user not found")
)

// FollowUser makes followerID follow followeeID. Idempotent (following someone
// you already follow is a no-op). Rejects self-follow; a non-existent followee
// yields ErrUserNotFound.
func FollowUser(ctx context.Context, q sqlc.Querier, followerID, followeeID int64) error {
	if followerID == followeeID {
		return ErrCannotFollowSelf // domain rule: you can't follow yourself
	}
	if err := q.FollowUser(ctx, sqlc.FollowUserParams{
		FollowerID: followerID,
		FolloweeID: followeeID,
	}); err != nil {
		// 23503 = FK violation → the followee user doesn't exist.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return ErrUserNotFound
		}
		return err
	}
	return nil
}

// UnfollowUser removes the follow edge. Idempotent: unfollowing someone you don't
// follow deletes 0 rows and is a successful no-op (the DELETE returns no error).
func UnfollowUser(ctx context.Context, q sqlc.Querier, followerID, followeeID int64) error {
	return q.UnfollowUser(ctx, sqlc.UnfollowUserParams{
		FollowerID: followerID,
		FolloweeID: followeeID,
	})
}
