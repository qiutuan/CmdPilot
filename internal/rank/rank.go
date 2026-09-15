// Package rank implements CmdPilot's usage-frequency ordering formula
// ("越用越懂你").
//
// score = log(1 + count) * decay(age, halfLife=30d) * contextBonus(1.5)
//
// decay = 0.5^(age / halfLife). All functions are pure and unit-tested.
package rank

import (
	"math"
	"time"
)

// HalfLifeDays is the popularity half-life: usage weight halves after 30 days.
const HalfLifeDays = 30.0

// ContextBonus is the multiplier applied when context matches
// (same directory / same prefix / chain relationship).
const ContextBonus = 1.5

// decay returns the time-decay factor in [0,1] for an age.
func decay(age time.Duration) float64 {
	half := HalfLifeDays * 24 * float64(time.Hour)
	if half <= 0 {
		return 0
	}
	return math.Pow(0.5, float64(age)/half)
}

// Score computes the ranking score for a command usage record.
// contextMatch toggles the x1.5 bonus (per config recommend_weighting).
func Score(count int, lastUsed, now time.Time, contextMatch bool) float64 {
	if count <= 0 {
		return 0
	}
	if now.Before(lastUsed) {
		now = lastUsed // never negative age
	}
	s := math.Log1p(float64(count)) * decay(now.Sub(lastUsed))
	if contextMatch {
		s *= ContextBonus
	}
	return s
}

// TopN reorders candidates by (rankScore desc, then matchScore desc) and
// returns up to n. It is a stable selection: candidates already sorted by
// match quality keep relative order within equal rank scores.
func TopN(cands []Candidate, n int) []Candidate {
	if n <= 0 {
		return nil
	}
	if len(cands) <= n {
		out := make([]Candidate, len(cands))
		copy(out, cands)
		return out
	}
	// Simple O(n log n) sort; candidate counts are tiny (< 1000).
	sorted := make([]Candidate, len(cands))
	copy(sorted, cands)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0; j-- {
			if sorted[j].RankScore > sorted[j-1].RankScore ||
				(sorted[j].RankScore == sorted[j-1].RankScore && sorted[j].MatchScore > sorted[j-1].MatchScore) {
				sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
			} else {
				break
			}
		}
	}
	return sorted[:n]
}

// Candidate is a ranked completion candidate.
type Candidate struct {
	Text       string  // candidate text (command name / suffix)
	RankScore  float64 // frequency-based score (0 if no usage)
	MatchScore float64 // match quality score
	Source     string  // provenance: local|history|favorite|ai
}
