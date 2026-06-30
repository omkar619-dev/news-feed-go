package search

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/pgvector/pgvector-go"

	"github.com/omkar619-dev/news-feed-go/internal/embed"
	"github.com/omkar619-dev/news-feed-go/internal/repository/postgres/sqlc"
)

// rrfK is the Reciprocal Rank Fusion dampening constant (the value from the
// original RRF paper). Larger k = flatter weighting near the top; it stops the
// #1 result of a single arm from dominating, so a post has to rank well in BOTH
// arms to win.
const rrfK = 60

// candidatePool is how many results EACH arm contributes before fusion. We
// over-fetch (50) so good cross-arm matches aren't cut off before they can be
// fused, then trim to the caller's limit at the end.
const candidatePool = 50

// rrfFuse merges ranked result lists by Reciprocal Rank Fusion.
//
// Each input list is already in rank order (best first). A post's fused score
// is the sum, over every list it appears in, of 1/(k + rank) — rank starting
// at 1. RRF deliberately IGNORES each arm's raw score: cosine distance and
// ts_rank live on completely different scales, so there's nothing sane to add
// or normalise. Using only POSITION sidesteps that entirely. A post that ranks
// high in both arms beats one that's #1 in a single arm.
func rrfFuse(lists ...[]sqlc.Post) []sqlc.Post {
	score := make(map[int64]float64)
	posts := make(map[int64]sqlc.Post)
	for _, list := range lists {
		for i, p := range list {
			rank := i + 1
			score[p.ID] += 1.0 / float64(rrfK+rank)
			posts[p.ID] = p // keep the row so we can return it after sorting
		}
	}

	fused := make([]sqlc.Post, 0, len(posts))
	for id := range posts {
		fused = append(fused, posts[id])
	}
	sort.Slice(fused, func(a, b int) bool {
		return score[fused[a].ID] > score[fused[b].ID]
	})
	return fused
}

// NewHybridSearchHandler: PUBLIC — GET /search/hybrid?q=...
// Runs a KEYWORD (full-text) search and a SEMANTIC (vector) search, then fuses
// their rankings with RRF. Keyword nails exact terms (names, jargon, codes);
// vector nails meaning (paraphrases, synonyms). Each is blind where the other
// sees — fusing covers both blind spots.
func NewHybridSearchHandler(queries sqlc.Querier, embedder *embed.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		if q == "" {
			errorJSON(w, http.StatusBadRequest, "query parameter 'q' is required")
			return
		}
		limit, ok := parseLimit(r)
		if !ok {
			errorJSON(w, http.StatusBadRequest, "invalid limit")
			return
		}

		// mode lets the eval harness isolate the keyword arm's contribution:
		//   hybrid (default) = keyword + semantic, RRF-fused
		//   semantic         = semantic arm ONLY (the controlled baseline)
		//   keyword          = keyword arm ONLY
		// A clean ablation needs the baseline and the treatment to share the
		// SAME semantic component — exactly mode=semantic vs mode=hybrid.
		mode := r.URL.Query().Get("mode")
		if mode == "" {
			mode = "hybrid"
		}
		if mode != "hybrid" && mode != "semantic" && mode != "keyword" {
			errorJSON(w, http.StatusBadRequest, "mode must be hybrid, semantic, or keyword")
			return
		}

		var lists [][]sqlc.Post

		// KEYWORD arm (exact terms). No Ollama needed.
		if mode == "hybrid" || mode == "keyword" {
			keywordRows, err := queries.KeywordCandidates(r.Context(), sqlc.KeywordCandidatesParams{
				Query:       q,
				ResultLimit: candidatePool,
			})
			if err != nil {
				errorJSON(w, http.StatusInternalServerError, "search failed")
				return
			}
			lists = append(lists, keywordRows)
		}

		// SEMANTIC arm (meaning). Embed the query, then nearest-neighbour. In
		// hybrid mode this DEGRADES GRACEFULLY: if Ollama is down we still
		// return the keyword results.
		if mode == "hybrid" || mode == "semantic" {
			if vec, err := embedder.Embed(r.Context(), q); err == nil {
				semanticRows, err := queries.SemanticCandidates(r.Context(), sqlc.SemanticCandidatesParams{
					QueryEmbedding: pgvector.NewVector(vec),
					ResultLimit:    candidatePool,
				})
				if err == nil {
					lists = append(lists, semanticRows)
				}
			}
		}

		// FUSE the arms by rank, then trim to the requested page size.
		fused := rrfFuse(lists...)
		if len(fused) > int(limit) {
			fused = fused[:limit]
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(SearchResponse{Posts: mapPosts(fused)})
	}
}
