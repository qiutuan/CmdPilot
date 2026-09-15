// Package match implements the local candidate scoring algorithms as pure
// functions: exact, prefix, substring/fuzzy (fzf-style) and edit-distance
// tiers. Every function here is deterministic and side-effect free so it can
// be unit-tested and benchmarked in isolation.
package match

import "strings"

// Tier describes how a candidate matched the query.
type Tier int

// Match tiers, from strongest to weakest.
const (
	TierNone   Tier = 0 // no match
	TierEdit   Tier = 1 // edit distance <= 2 (typo tolerance)
	TierFuzzy  Tier = 2 // substring / smart fuzzy match
	TierPrefix Tier = 3 // plain prefix match
	TierExact  Tier = 4 // exact match
)

// Result is the outcome of evaluating one candidate.
type Result struct {
	Tier  Tier
	Score float64
}

// None returns a non-matching Result.
func None() Result { return Result{Tier: TierNone, Score: 0} }

// Match evaluates query against candidate and returns the best tier + score.
// Matching is case-insensitive. Multi-word names (e.g. "git commit") are
// matched as a whole against the input.
func Match(query, candidate string) Result {
	q, c := strings.ToLower(strings.TrimSpace(query)), strings.ToLower(strings.TrimSpace(candidate))
	if q == "" || c == "" {
		return None()
	}
	if q == c {
		return Result{Tier: TierExact, Score: 1.0}
	}
	if strings.HasPrefix(c, q) {
		return Result{Tier: TierPrefix, Score: prefixScore(q, c)}
	}
	if fs := fuzzyScore(q, c); fs > 0 {
		return Result{Tier: TierFuzzy, Score: fs}
	}
	if len(q) >= 4 && levenshtein(q, c) <= 2 {
		return Result{Tier: TierEdit, Score: editScore(q, c)}
	}
	return None()
}

// prefixScore rewards longer matched prefixes relative to candidate length.
func prefixScore(q, c string) float64 {
	return 0.5 + 0.5*float64(len(q))/float64(len(c))
}

// isWordBoundary reports whether prev ends a word (start of input or after
// space / dash / underscore / slash / dot / colon / backslash).
func isWordBoundary(prev byte) bool {
	return prev == 0 || prev == ' ' || prev == '-' || prev == '_' || prev == '/' ||
		prev == '\\' || prev == '.' || prev == ':' || prev == '@'
}

// fuzzyScore implements an fzf-inspired scorer: every query character must
// appear in order; consecutive runs and word-initial matches get bonuses.
// Returns 0 when the query cannot be matched in order.
func fuzzyScore(q, c string) float64 {
	if len(q) > len(c) {
		return 0
	}
	qi := 0
	run := 0
	score := 0.0
	var prev byte
	for ci := 0; ci < len(c) && qi < len(q); ci++ {
		if c[ci] != q[qi] {
			prev = c[ci]
			continue
		}
		qi++
		if run > 0 {
			run++
			score += 2 // consecutive run bonus
		} else {
			run = 1
			score += 1
		}
		if isWordBoundary(prev) {
			score += 2 // word-initial bonus
		}
		prev = c[ci]
	}
	if qi < len(q) {
		return 0 // characters not found in order
	}
	// Normalize by the theoretical maximum: each of the len(q) matched chars
	// can score at most 3 (1 base + 2 run), plus at most 2 boundary bonus
	// per matched char (rare in practice, so it rewards compact matches).
	maxScore := 3*float64(len(q)) + 2*float64(len(q))
	if maxScore <= 0 {
		return 0
	}
	s := score / maxScore
	if s > 1 {
		s = 1
	}
	return s
}

// levenshtein computes the classic edit distance (insert/delete/substitute).
func levenshtein(a, b string) int {
	// Early exit on length difference > 2 (caller only allows <= 2).
	if abs(len(a)-len(b)) > 2 {
		return abs(len(a) - len(b))
	}
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := 0; j <= len(rb); j++ {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min3(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// editScore gives a mild score for typo-tolerant matches (closer = higher).
func editScore(q, c string) float64 {
	d := levenshtein(q, c)
	return 0.1 + 0.15*float64(2-d) // d in {1,2}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func min3(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}

// BestOf selects the best result from a slice (ties keep the first).
func BestOf(results []Result) Result {
	best := None()
	for _, r := range results {
		if r.Tier > best.Tier || (r.Tier == best.Tier && r.Score > best.Score) {
			best = r
		}
	}
	return best
}
