// Command eval scores search QUALITY (not correctness) against a labeled set.
//
// For each query in eval/labels.json it hits BOTH /search (pure semantic) and
// /search/hybrid (keyword + semantic, RRF-fused), grades the returned ranking
// with Precision@k, MRR and nDCG@k, and prints a semantic-vs-hybrid comparison.
// The "lift" column is the whole point: did adding the keyword arm measurably
// improve ranking quality?
//
// Run with the API server up:
//   go run ./cmd/eval
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// labelSet / query mirror eval/labels.json. Relevant maps a post id (string,
// because JSON object keys are always strings) to its relevance grade.
type labelSet struct {
	K       int     `json:"k"`
	Queries []query `json:"queries"`
}
type query struct {
	Q        string         `json:"q"`
	Relevant map[string]int `json:"relevant"`
}

// searchResponse: we only care about the ORDER of ids the endpoint returned.
type searchResponse struct {
	Posts []struct {
		ID int64 `json:"id"`
	} `json:"posts"`
}

var httpClient = &http.Client{Timeout: 30 * time.Second} // /search can be slow on Ollama cold start

func main() {
	base := flag.String("base", envOr("BASE_URL", "http://localhost:3000"), "API base URL")
	labelsPath := flag.String("labels", "eval/labels.json", "path to labels JSON")
	flag.Parse()

	raw, err := os.ReadFile(*labelsPath)
	if err != nil {
		die("reading labels: %v", err)
	}
	var ls labelSet
	if err := json.Unmarshal(raw, &ls); err != nil {
		die("parsing labels: %v", err)
	}
	k := ls.K
	if k == 0 {
		k = 10
	}

	fmt.Printf("Evaluating %d queries at k=%d against %s\n", len(ls.Queries), k, *base)
	fmt.Printf("baseline = semantic arm only; treatment = hybrid (SAME semantic arm + keyword, RRF-fused)\n\n")

	// Per-query nDCG table (so you can SEE which queries hybrid helped).
	fmt.Printf("%-44s %9s %9s\n", "query", "sem nDCG", "hyb nDCG")
	fmt.Println(strings.Repeat("-", 64))

	var semP, semRR, semN, hybP, hybRR, hybN float64
	for _, qy := range ls.Queries {
		semIDs, err := runSearch(*base, "/search/hybrid", "semantic", qy.Q, k)
		if err != nil {
			die("query %q (semantic): %v", qy.Q, err)
		}
		hybIDs, err := runSearch(*base, "/search/hybrid", "hybrid", qy.Q, k)
		if err != nil {
			die("query %q (hybrid): %v", qy.Q, err)
		}

		sN := ndcgAtK(semIDs, qy.Relevant, k)
		hN := ndcgAtK(hybIDs, qy.Relevant, k)
		semP += precisionAtK(semIDs, qy.Relevant, k)
		semRR += reciprocalRank(semIDs, qy.Relevant)
		semN += sN
		hybP += precisionAtK(hybIDs, qy.Relevant, k)
		hybRR += reciprocalRank(hybIDs, qy.Relevant)
		hybN += hN

		fmt.Printf("%-44s %9.3f %9.3f\n", truncate(qy.Q, 44), sN, hN)
	}

	n := float64(len(ls.Queries))
	fmt.Printf("\n%-10s %9s %9s %9s\n", "metric", "semantic", "hybrid", "lift")
	fmt.Println(strings.Repeat("-", 42))
	row := func(name string, s, h float64) {
		fmt.Printf("%-10s %9.3f %9.3f %+9.3f\n", name, s, h, h-s)
	}
	row(fmt.Sprintf("P@%d", k), semP/n, hybP/n)
	row("MRR", semRR/n, hybRR/n)
	row(fmt.Sprintf("nDCG@%d", k), semN/n, hybN/n)
}

// runSearch hits an endpoint in a given mode and returns the ordered post ids.
func runSearch(base, path, mode, q string, k int) ([]int64, error) {
	u := fmt.Sprintf("%s%s?q=%s&limit=%d&mode=%s", base, path, url.QueryEscape(q), k, mode)
	resp, err := httpClient.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var sr searchResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, err
	}
	ids := make([]int64, len(sr.Posts))
	for i, p := range sr.Posts {
		ids[i] = p.ID
	}
	return ids, nil
}

// precisionAtK: fraction of the top-k results that are relevant (grade > 0).
// Cares about how much junk is in the top k; ignores order within it.
func precisionAtK(ids []int64, rel map[string]int, k int) float64 {
	hits := 0
	for i := 0; i < k && i < len(ids); i++ {
		if rel[fmt.Sprint(ids[i])] > 0 {
			hits++
		}
	}
	return float64(hits) / float64(k)
}

// reciprocalRank: 1 / (rank of the FIRST relevant result), 0 if none in the list.
func reciprocalRank(ids []int64, rel map[string]int) float64 {
	for i, id := range ids {
		if rel[fmt.Sprint(id)] > 0 {
			return 1.0 / float64(i+1)
		}
	}
	return 0
}

// dcgAtK: Σ grade_i / log2(rank_i + 1). rank starts at 1, so position i (0-based)
// divides by log2(i+2). Lower positions are discounted by a bigger number.
func dcgAtK(ids []int64, rel map[string]int, k int) float64 {
	var dcg float64
	for i := 0; i < k && i < len(ids); i++ {
		grade := float64(rel[fmt.Sprint(ids[i])])
		dcg += grade / math.Log2(float64(i+2))
	}
	return dcg
}

// idcgAtK: DCG of the IDEAL ranking — every known grade sorted best-first, top k.
// This is the perfect score we normalise against.
func idcgAtK(rel map[string]int, k int) float64 {
	grades := make([]int, 0, len(rel))
	for _, g := range rel {
		grades = append(grades, g)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(grades)))
	var idcg float64
	for i := 0; i < k && i < len(grades); i++ {
		idcg += float64(grades[i]) / math.Log2(float64(i+2))
	}
	return idcg
}

// ndcgAtK = DCG / IDCG → 0..1, comparable across queries. 1.0 = perfect order.
func ndcgAtK(ids []int64, rel map[string]int, k int) float64 {
	idcg := idcgAtK(rel, k)
	if idcg == 0 {
		return 0
	}
	return dcgAtK(ids, rel, k) / idcg
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
