package post

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/omkar619-dev/news-feed-go/internal/cursor"
	"github.com/omkar619-dev/news-feed-go/internal/repository/postgres/sqlc"
)

const (
	defaultPageLimit = 20
	maxPageLimit     = 50
)

// ListPostsResponse is one page of posts plus the bookmark for the NEXT page.
// NextCursor is "" when there are no more pages.
type ListPostsResponse struct {
	Posts      []PostResponse `json:"posts"`
	NextCursor string         `json:"next_cursor"`
}

// NewListUserPostsHandler lists a user's posts, newest first, cursor-paginated.
// PUBLIC route (no auth) — anyone can browse a user's posts.
func NewListUserPostsHandler(queries sqlc.Querier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 1. Whose posts? {id} from the path.
		authorID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			errorJSON(w, http.StatusBadRequest, "invalid user id")
			return
		}

		// 2. Page size from ?limit= (optional). Default 20, hard cap 50.
		limit := defaultPageLimit
		if s := r.URL.Query().Get("limit"); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 {
				errorJSON(w, http.StatusBadRequest, "invalid limit")
				return
			}
			if n > maxPageLimit {
				n = maxPageLimit
			}
			limit = n
		}

		// Fetch ONE extra row (limit+1) to detect whether a next page exists.
		fetch := int32(limit + 1)

		// 3. First page (no cursor) vs next page (cursor present).
		//    NOTE: the query param is named "cursor"; we store it in `cur`
		//    so it doesn't shadow the imported `cursor` package.
		var rows []sqlc.Post
		cur := r.URL.Query().Get("cursor")
		if cur == "" {
			rows, err = queries.ListUserPostsFirst(r.Context(), sqlc.ListUserPostsFirstParams{
				AuthorID:  authorID,
				PageLimit: fetch,
			})
		} else {
			t, id, derr := cursor.Decode(cur)
			if derr != nil {
				errorJSON(w, http.StatusBadRequest, "invalid cursor")
				return
			}
			rows, err = queries.ListUserPostsAfter(r.Context(), sqlc.ListUserPostsAfterParams{
				AuthorID:        authorID,
				CursorCreatedAt: pgtype.Timestamptz{Time: t, Valid: true},
				CursorID:        id,
				PageLimit:       fetch,
			})
		}
		if err != nil {
			errorJSON(w, http.StatusInternalServerError, "could not list posts")
			return
		}

		// 4. Did the peek row come back? Then there's another page after this.
		hasMore := len(rows) > limit
		if hasMore {
			rows = rows[:limit]
		}

		// 5. Map DB rows → response shape.
		posts := make([]PostResponse, 0, len(rows))
		for _, p := range rows {
			posts = append(posts, PostResponse{
				ID:        p.ID,
				AuthorID:  p.AuthorID,
				Content:   p.Content,
				CreatedAt: p.CreatedAt.Time,
			})
		}

		// 6. Bookmark for the next page = the last post on THIS page (if any more).
		next := ""
		if hasMore && len(rows) > 0 {
			last := rows[len(rows)-1]
			next = cursor.Encode(last.CreatedAt.Time, last.ID)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(ListPostsResponse{
			Posts:      posts,
			NextCursor: next,
		})
	}
}
