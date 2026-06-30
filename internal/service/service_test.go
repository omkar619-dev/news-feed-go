package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/omkar619-dev/news-feed-go/internal/mq"
	"github.com/omkar619-dev/news-feed-go/internal/repository/postgres/sqlc"
)

// fakeQuerier is a hand-written test double for sqlc.Querier. We EMBED the
// interface, so the struct already satisfies all ~35 methods for free — then we
// override only the handful the functions under test actually call. Any method we
// DON'T override is the nil embedded interface; calling it panics, which is what we
// want: it loudly flags a test that reached a query path we didn't expect.
//
// The override methods use a POINTER receiver (so InsertOutboxEvent can append to
// the capture slice), which is why every test passes &fakeQuerier{…}.
type fakeQuerier struct {
	sqlc.Querier // embedded interface — supplies every method we don't define below

	createPostFn     func(sqlc.CreatePostParams) (sqlc.Post, error)
	outbox           []sqlc.InsertOutboxEventParams // every outbox write, captured in order
	getPostByIDFn    func(int64) (sqlc.Post, error)
	deletePostFn     func(sqlc.DeletePostParams) error
	createCommentFn  func(sqlc.CreateCommentParams) (sqlc.Comment, error)
	createCommentArg sqlc.CreateCommentParams // the last CreateComment arg, captured
	followUserFn     func(sqlc.FollowUserParams) error
	unfollowUserFn   func(sqlc.UnfollowUserParams) error
}

func (f *fakeQuerier) CreatePost(_ context.Context, arg sqlc.CreatePostParams) (sqlc.Post, error) {
	return f.createPostFn(arg)
}

func (f *fakeQuerier) InsertOutboxEvent(_ context.Context, arg sqlc.InsertOutboxEventParams) error {
	f.outbox = append(f.outbox, arg) // capture instead of writing to a DB
	return nil
}

func (f *fakeQuerier) GetPostByID(_ context.Context, id int64) (sqlc.Post, error) {
	return f.getPostByIDFn(id)
}

func (f *fakeQuerier) DeletePost(_ context.Context, arg sqlc.DeletePostParams) error {
	return f.deletePostFn(arg)
}

func (f *fakeQuerier) CreateComment(_ context.Context, arg sqlc.CreateCommentParams) (sqlc.Comment, error) {
	f.createCommentArg = arg // capture so a test can assert the parent mapping
	return f.createCommentFn(arg)
}

func (f *fakeQuerier) FollowUser(_ context.Context, arg sqlc.FollowUserParams) error {
	return f.followUserFn(arg)
}

func (f *fakeQuerier) UnfollowUser(_ context.Context, arg sqlc.UnfollowUserParams) error {
	return f.unfollowUserFn(arg)
}

// ── CreatePost: the domain rules + the transactional-outbox guarantee ──

func TestCreatePost(t *testing.T) {
	ctx := context.Background()

	t.Run("rejects empty content", func(t *testing.T) {
		_, err := CreatePost(ctx, &fakeQuerier{}, 7, "   ")
		if !errors.Is(err, ErrEmptyContent) {
			t.Fatalf("want ErrEmptyContent, got %v", err)
		}
	})

	t.Run("rejects content over 280 chars", func(t *testing.T) {
		_, err := CreatePost(ctx, &fakeQuerier{}, 7, strings.Repeat("a", 281))
		if !errors.Is(err, ErrContentTooLong) {
			t.Fatalf("want ErrContentTooLong, got %v", err)
		}
	})

	t.Run("writes the post AND a post.created outbox event", func(t *testing.T) {
		fq := &fakeQuerier{
			createPostFn: func(arg sqlc.CreatePostParams) (sqlc.Post, error) {
				return sqlc.Post{ID: 99, AuthorID: arg.AuthorID, Content: arg.Content}, nil
			},
		}

		post, err := CreatePost(ctx, fq, 7, "hello world")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if post.ID != 99 {
			t.Errorf("post.ID = %d, want 99", post.ID)
		}

		// THE point of the outbox: exactly one event, of the right type, carrying this
		// post's id — written on the same Querier (so, in prod, the same transaction).
		if len(fq.outbox) != 1 {
			t.Fatalf("outbox writes = %d, want 1", len(fq.outbox))
		}
		if fq.outbox[0].EventType != "post.created" {
			t.Errorf("event type = %q, want post.created", fq.outbox[0].EventType)
		}
		var evt mq.PostCreatedEvent
		if err := json.Unmarshal(fq.outbox[0].Payload, &evt); err != nil {
			t.Fatalf("outbox payload is not valid JSON: %v", err)
		}
		if evt.PostID != 99 || evt.AuthorID != 7 {
			t.Errorf("event = %+v, want {PostID:99 AuthorID:7}", evt)
		}
	})

	t.Run("no outbox event if the post insert fails", func(t *testing.T) {
		dbErr := errors.New("db down")
		fq := &fakeQuerier{
			createPostFn: func(sqlc.CreatePostParams) (sqlc.Post, error) { return sqlc.Post{}, dbErr },
		}

		_, err := CreatePost(ctx, fq, 7, "hello")
		if !errors.Is(err, dbErr) {
			t.Fatalf("want the db error, got %v", err)
		}
		if len(fq.outbox) != 0 {
			t.Errorf("outbox must stay empty when the post insert fails, got %d writes", len(fq.outbox))
		}
	})
}

// ── DeletePost: the ownership check ──

func TestDeletePost(t *testing.T) {
	ctx := context.Background()

	t.Run("missing post → ErrPostNotFound", func(t *testing.T) {
		fq := &fakeQuerier{
			getPostByIDFn: func(int64) (sqlc.Post, error) { return sqlc.Post{}, pgx.ErrNoRows },
		}
		if err := DeletePost(ctx, fq, 7, 99); !errors.Is(err, ErrPostNotFound) {
			t.Fatalf("want ErrPostNotFound, got %v", err)
		}
	})

	t.Run("someone else's post → ErrNotPostOwner, and no DELETE runs", func(t *testing.T) {
		deleteCalled := false
		fq := &fakeQuerier{
			getPostByIDFn: func(int64) (sqlc.Post, error) {
				return sqlc.Post{ID: 99, AuthorID: 1234}, nil // owned by user 1234
			},
			deletePostFn: func(sqlc.DeletePostParams) error { deleteCalled = true; return nil },
		}
		if err := DeletePost(ctx, fq, 7, 99); !errors.Is(err, ErrNotPostOwner) { // user 7 ≠ owner
			t.Fatalf("want ErrNotPostOwner, got %v", err)
		}
		if deleteCalled {
			t.Error("DeletePost must NOT run when the caller isn't the owner")
		}
	})

	t.Run("owner → deletes with owner-scoped params", func(t *testing.T) {
		var got sqlc.DeletePostParams
		fq := &fakeQuerier{
			getPostByIDFn: func(int64) (sqlc.Post, error) { return sqlc.Post{ID: 99, AuthorID: 7}, nil },
			deletePostFn:  func(arg sqlc.DeletePostParams) error { got = arg; return nil },
		}
		if err := DeletePost(ctx, fq, 7, 99); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.ID != 99 || got.AuthorID != 7 {
			t.Errorf("delete params = %+v, want {ID:99 AuthorID:7}", got)
		}
	})
}

// ── AddComment: empty-guard, FK→sentinel translation, and parent mapping ──

func TestAddComment(t *testing.T) {
	ctx := context.Background()

	t.Run("rejects empty content", func(t *testing.T) {
		_, err := AddComment(ctx, &fakeQuerier{}, 1, 7, nil, "   ")
		if !errors.Is(err, ErrEmptyContent) {
			t.Fatalf("want ErrEmptyContent, got %v", err)
		}
	})

	t.Run("FK violation → ErrCommentTargetMissing", func(t *testing.T) {
		fq := &fakeQuerier{
			createCommentFn: func(sqlc.CreateCommentParams) (sqlc.Comment, error) {
				return sqlc.Comment{}, &pgconn.PgError{Code: "23503"} // FK violation
			},
		}
		_, err := AddComment(ctx, fq, 1, 7, nil, "reply to a ghost")
		if !errors.Is(err, ErrCommentTargetMissing) {
			t.Fatalf("want ErrCommentTargetMissing, got %v", err)
		}
	})

	t.Run("top-level comment maps to a NULL parent", func(t *testing.T) {
		fq := &fakeQuerier{
			createCommentFn: func(sqlc.CreateCommentParams) (sqlc.Comment, error) {
				return sqlc.Comment{ID: 5}, nil
			},
		}
		if _, err := AddComment(ctx, fq, 1, 7, nil, "top level"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if fq.createCommentArg.ParentID.Valid {
			t.Error("a top-level comment must map to a NULL parent (Valid=false)")
		}
	})

	t.Run("reply carries its parent id", func(t *testing.T) {
		parent := int64(42)
		fq := &fakeQuerier{
			createCommentFn: func(sqlc.CreateCommentParams) (sqlc.Comment, error) {
				return sqlc.Comment{ID: 6}, nil
			},
		}
		if _, err := AddComment(ctx, fq, 1, 7, &parent, "a reply"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !fq.createCommentArg.ParentID.Valid || fq.createCommentArg.ParentID.Int64 != 42 {
			t.Errorf("reply must carry parent 42, got %+v", fq.createCommentArg.ParentID)
		}
	})
}

// ── FollowUser: the self-follow rule + FK→sentinel translation ──

func TestFollowUser(t *testing.T) {
	ctx := context.Background()

	t.Run("self-follow → ErrCannotFollowSelf, before any query", func(t *testing.T) {
		called := false
		fq := &fakeQuerier{followUserFn: func(sqlc.FollowUserParams) error { called = true; return nil }}
		if err := FollowUser(ctx, fq, 7, 7); !errors.Is(err, ErrCannotFollowSelf) {
			t.Fatalf("want ErrCannotFollowSelf, got %v", err)
		}
		if called {
			t.Error("self-follow must be rejected BEFORE touching the DB")
		}
	})

	t.Run("unknown followee (FK) → ErrUserNotFound", func(t *testing.T) {
		fq := &fakeQuerier{
			followUserFn: func(sqlc.FollowUserParams) error { return &pgconn.PgError{Code: "23503"} },
		}
		if err := FollowUser(ctx, fq, 7, 999); !errors.Is(err, ErrUserNotFound) {
			t.Fatalf("want ErrUserNotFound, got %v", err)
		}
	})

	t.Run("happy path", func(t *testing.T) {
		fq := &fakeQuerier{followUserFn: func(sqlc.FollowUserParams) error { return nil }}
		if err := FollowUser(ctx, fq, 7, 8); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

// ── UnfollowUser: a thin pass-through that must propagate the querier's error ──

func TestUnfollowUser(t *testing.T) {
	ctx := context.Background()
	wantErr := errors.New("boom")
	fq := &fakeQuerier{unfollowUserFn: func(sqlc.UnfollowUserParams) error { return wantErr }}
	if err := UnfollowUser(ctx, fq, 7, 8); !errors.Is(err, wantErr) {
		t.Fatalf("UnfollowUser should propagate the querier error, got %v", err)
	}
}
