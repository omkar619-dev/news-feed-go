package search

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/omkar619-dev/news-feed-go/internal/repository/postgres/sqlc"
)

// defaultDupDistance is the cosine-distance cutoff below which two posts count as
// near-duplicates. Cosine distance runs 0 (identical meaning) .. 2 (opposite);
// near-identical paraphrases land well under ~0.2 with all-minilm. This is a
// TUNABLE default — too tight misses reworded copies, too loose flags merely
// similar posts. Override per-request with ?threshold=.
const defaultDupDistance = 0.25

// DuplicateItem is one near-duplicate, with how close it is (so a caller — or you,
// tuning the threshold — can see the actual distances).
type DuplicateItem struct {
	ID       int64   `json:"id"`
	AuthorID int64   `json:"author_id"`
	Content  string  `json:"content"`
	Distance float64 `json:"distance"`
}

// DuplicatesResponse is the cluster of near-duplicates around a post.
type DuplicatesResponse struct {
	Duplicates []DuplicateItem `json:"duplicates"`
}

// NewDuplicatesHandler: PUBLIC — GET /posts/{id}/duplicates?threshold=&limit=
// The astroturf/spam-cluster defence. Fifty near-identical posts (the classic
// "flood the feed to fake consensus" attack) collapse to a tight cluster in
// embedding space; this surfaces that cluster by absolute cosine distance.
func NewDuplicatesHandler(queries sqlc.Querier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			errorJSON(w, http.StatusBadRequest, "invalid post id")
			return
		}

		// Optional ?threshold= override (cosine distance, 0..2). Smaller = stricter.
		maxDist := defaultDupDistance
		if s := r.URL.Query().Get("threshold"); s != "" {
			t, err := strconv.ParseFloat(s, 64)
			if err != nil || t <= 0 || t > 2 {
				errorJSON(w, http.StatusBadRequest, "threshold must be a number between 0 and 2")
				return
			}
			maxDist = t
		}

		limit, ok := parseLimit(r)
		if !ok {
			errorJSON(w, http.StatusBadRequest, "invalid limit")
			return
		}

		rows, err := queries.FindNearDuplicates(r.Context(), sqlc.FindNearDuplicatesParams{
			PostID:      id,
			MaxDistance: maxDist,
			ResultLimit: limit,
		})
		if err != nil {
			errorJSON(w, http.StatusInternalServerError, "could not find duplicates")
			return
		}

		out := make([]DuplicateItem, 0, len(rows))
		for _, d := range rows {
			out = append(out, DuplicateItem{
				ID:       d.ID,
				AuthorID: d.AuthorID,
				Content:  d.Content,
				Distance: d.Distance,
			})
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(DuplicatesResponse{Duplicates: out})
	}
}
