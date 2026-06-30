package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/omkar619-dev/news-feed-go/internal/mq"
	"github.com/omkar619-dev/news-feed-go/internal/repository/postgres/sqlc"
)

const maxPostLength = 280

var (
	ErrContentTooLong = errors.New("content exceeds 280 characters")
	ErrNotPostOwner   = errors.New("not the post owner")
)

// CreatePost validates the content, inserts the post, and writes a "post.created"
// outbox event — ALL on the caller's Querier. The caller owns the transaction:
// it passes a tx-bound Querier so the post + outbox commit atomically, and so the
// HTTP handler can add its idempotency-key insert to the SAME transaction. The TUI
// just wraps this in a plain tx with nothing extra. Returns the created post.
func CreatePost(ctx context.Context, q sqlc.Querier, authorID int64, content string) (sqlc.Post, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return sqlc.Post{}, ErrEmptyContent // reused from comment.go — same domain rule
	}
	// RuneCountInString counts CHARACTERS, not bytes (an emoji is 4 bytes, 1 char).
	if utf8.RuneCountInString(content) > maxPostLength {
		return sqlc.Post{}, ErrContentTooLong
	}

	post, err := q.CreatePost(ctx, sqlc.CreatePostParams{AuthorID: authorID, Content: content})
	if err != nil {
		return sqlc.Post{}, err
	}

	// The outbox event — written in the SAME tx as the post (the caller's tx), so
	// the fan-out/embedding event can never be lost. This is the transactional outbox.
	payload, err := json.Marshal(mq.PostCreatedEvent{PostID: post.ID, AuthorID: authorID})
	if err != nil {
		return sqlc.Post{}, err
	}
	if err := q.InsertOutboxEvent(ctx, sqlc.InsertOutboxEventParams{
		EventType: "post.created",
		Payload:   payload,
	}); err != nil {
		return sqlc.Post{}, err
	}
	return post, nil
}

// DeletePost removes a post the user OWNS. It fetches first so the adapter can
// tell "no such post" (ErrPostNotFound) from "exists but not yours"
// (ErrNotPostOwner); the owner-scoped DELETE also closes the race between the
// fetch and the delete.
func DeletePost(ctx context.Context, q sqlc.Querier, userID, postID int64) error {
	post, err := q.GetPostByID(ctx, postID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPostNotFound // reused from like.go
		}
		return err
	}
	if post.AuthorID != userID {
		return ErrNotPostOwner
	}
	if err := q.DeletePost(ctx, sqlc.DeletePostParams{ID: postID, AuthorID: userID}); err != nil {
		return err
	}
	return nil
}
