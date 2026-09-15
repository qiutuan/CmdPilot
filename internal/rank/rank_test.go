package rank

import (
	"math"
	"testing"
	"time"
)

// TestDecayHalvesAfterHalfLife verifies the half-life property.
func TestDecayHalvesAfterHalfLife(t *testing.T) {
	now := time.Now()
	d30 := now.Add(-30 * 24 * time.Hour)
	d60 := now.Add(-60 * 24 * time.Hour)
	d1 := now.Add(-24 * time.Hour)
	if math.Abs(decay(now.Sub(d30))-0.5) > 1e-9 {
		t.Errorf("decay(30d) = %v, want 0.5", decay(now.Sub(d30)))
	}
	if math.Abs(decay(now.Sub(d60))-0.25) > 1e-9 {
		t.Errorf("decay(60d) = %v, want 0.25", decay(now.Sub(d60)))
	}
	if decay(now.Sub(d1)) >= 0.99 {
		t.Errorf("decay(1d) too high: %v", decay(now.Sub(d1)))
	}
}

// TestScoreZeroCount verifies never-used commands score 0.
func TestScoreZeroCount(t *testing.T) {
	if s := Score(0, time.Now(), time.Now(), true); s != 0 {
		t.Errorf("Score(0) = %v, want 0", s)
	}
}

// TestScoreContextBonus verifies the x1.5 multiplier.
func TestScoreContextBonus(t *testing.T) {
	now := time.Now()
	base := Score(10, now.Add(-time.Hour), now, false)
	bonus := Score(10, now.Add(-time.Hour), now, true)
	if math.Abs(bonus/base-1.5) > 1e-9 {
		t.Errorf("context bonus ratio = %v, want 1.5", bonus/base)
	}
}

// TestScoreMonotonic verifies more usage => higher score (same age).
func TestScoreMonotonic(t *testing.T) {
	now := time.Now()
	s1 := Score(1, now.Add(-time.Hour), now, false)
	s5 := Score(5, now.Add(-time.Hour), now, false)
	s20 := Score(20, now.Add(-time.Hour), now, false)
	if !(s1 < s5 && s5 < s20) {
		t.Errorf("scores not monotonic: %v %v %v", s1, s5, s20)
	}
}

// TestScoreRecency verifies newer usage scores higher (same count).
func TestScoreRecency(t *testing.T) {
	now := time.Now()
	recent := Score(10, now.Add(-time.Hour), now, false)
	stale := Score(10, now.Add(-45*24*time.Hour), now, false)
	if !(recent > stale) {
		t.Errorf("recent %v should beat stale %v", recent, stale)
	}
}

// TestScoreFutureTimeClamped verifies future timestamps don't explode the score.
func TestScoreFutureTimeClamped(t *testing.T) {
	now := time.Now()
	s := Score(5, now.Add(24*time.Hour), now, false) // lastUsed in the future
	if s <= 0 || math.IsInf(s, 0) {
		t.Errorf("future lastUsed produced %v", s)
	}
}

// TestTopNOrdering verifies ranking puts high-frequency first and respects match ties.
func TestTopNOrdering(t *testing.T) {
	now := time.Now()
	cands := []Candidate{
		{Text: "git status", RankScore: 3.0, MatchScore: 0.9},
		{Text: "git stash", RankScore: 5.0, MatchScore: 0.5},
		{Text: "git switch", RankScore: 5.0, MatchScore: 0.8},
		{Text: "git show", RankScore: 1.0, MatchScore: 0.9},
	}
	top := TopN(cands, 2)
	if len(top) != 2 {
		t.Fatalf("TopN size = %d", len(top))
	}
	if top[0].Text != "git switch" || top[1].Text != "git stash" {
		t.Errorf("ordering wrong: %v %v", top[0].Text, top[1].Text)
	}
}

// TestTopNCaps verifies n bounds.
func TestTopNCaps(t *testing.T) {
	now := time.Now()
	c := []Candidate{{Text: "a", RankScore: Score(1, now, now, false)}}
	if TopN(c, 0) != nil {
		t.Error("TopN 0 should be nil")
	}
	if len(TopN(c, 10)) != 1 {
		t.Error("TopN larger than input should return all")
	}
}
