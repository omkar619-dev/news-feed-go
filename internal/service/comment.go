package service

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/omkar619-dev/news-feed-go/internal/repository/postgres/sqlc"
)

// Comment-domain sentinel errors. Adapters map them to their own output.
var (
	ErrEmptyContent         = errors.New("content is required")
	ErrCommentTargetMissing = errors.New("post or parent comment not found")
	ErrCommentNotFound      = errors.New("comment not found")
)

// toPgInt8 maps an optional *int64 (nil = absent) to the DB's nullable pgtype.Int8.
// This is the "three spellings of a maybe-absent number" translation, kept in the
// domain because the rule (a comment MAY have a parent) is a domain rule.
func toPgInt8(p *int64) pgtype.Int8 {
	if p == nil {
		return pgtype.Int8{} // Valid:false → SQL NULL → a top-level comment
	}
	return pgtype.Int8{Int64: *p, Valid: true}
}

// AddComment creates a comment on a post, or a reply if parentID != nil.
func AddComment(ctx context.Context, q sqlc.Querier, postID, authorID int64, parentID *int64, content string) (sqlc.Comment, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return sqlc.Comment{}, ErrEmptyContent
	}
	c, err := q.CreateComment(ctx, sqlc.CreateCommentParams{
		PostID:   postID,
		AuthorID: authorID,
		ParentID: toPgInt8(parentID),
		Content:  content,
	})
	if err != nil {
		// 23503 = FK violation → the post (or the parent comment) doesn't exist.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return sqlc.Comment{}, ErrCommentTargetMissing
		}
		return sqlc.Comment{}, err
	}
	return c, nil
}

// CommentTree returns a post's entire threaded comment tree, depth-first (the
// recursive-CTE query). A thin pass-through, but it keeps EVERY domain read going
// through the service so adapters never reach into sqlc directly.
func CommentTree(ctx context.Context, q sqlc.Querier, postID int64) ([]sqlc.GetCommentTreeRow, error) {
	return q.GetCommentTree(ctx, postID)
}

// DeleteComment removes a comment the user OWNS. The owner-scoped DELETE returns
// no row if the comment doesn't exist OR isn't theirs — both map to ErrCommentNotFound.
func DeleteComment(ctx context.Context, q sqlc.Querier, userID, commentID int64) error {
	if _, err := q.DeleteComment(ctx, sqlc.DeleteCommentParams{ID: commentID, AuthorID: userID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCommentNotFound
		}
		return err
	}
	return nil
}
