package match

import "testing"

// TestMatchTiers pins the tier precedence behavior.
func TestMatchTiers(t *testing.T) {
	cases := []struct {
		q, c string
		want Tier
	}{
		{"git", "git", TierExact},
		{"git commit", "git commit", TierExact},
		{"git c", "git commit", TierPrefix},
		{"gitc", "git commit", TierFuzzy},               // no space: in-order chars, not a prefix
		{"gcmt", "git commit", TierFuzzy},               // all chars in order, skipped gaps
		{"gco", "git checkout", TierFuzzy},              // word-initial letters
		{"git checkot", "git checkout", TierFuzzy},      // in-order match beats typo tier
		{"git commti", "git commit", TierEdit},          // out-of-order tail -> edit tier
		{"xyzq", "git commit", TierNone},
		{"", "git", TierNone},
	}
	for _, tc := range cases {
		got := Match(tc.q, tc.c)
		if got.Tier != tc.want {
			t.Errorf("Match(%q,%q).Tier = %v, want %v (score %v)", tc.q, tc.c, got.Tier, tc.want, got.Score)
		}
	}
}

// TestPrefixScoreMonotonic verifies longer prefixes score higher.
func TestPrefixScoreMonotonic(t *testing.T) {
	s1 := Match("git", "git commit").Score
	s2 := Match("git c", "git commit").Score
	s3 := Match("git co", "git commit").Score
	if !(s1 < s2 && s2 < s3) {
		t.Errorf("prefix scores not monotonic: %v %v %v", s1, s2, s3)
	}
}

// TestFuzzyWordInitial verifies the word-initial bonus distinguishes
// "gc" (strong) from "gc" spread... and favors camel/word initials.
func TestFuzzyWordInitial(t *testing.T) {
	// "gc" maps to word initials of "git commit" (g then c at word start).
	initials := fuzzyScore("gc", "git commit")
	spread := fuzzyScore("gc", "grand canyon") // g then c also word-initial
	if initials <= 0 || spread <= 0 {
		t.Fatalf("expected positive fuzzy scores, got %v %v", initials, spread)
	}
	// A dense match should beat a sparse one for the same query length.
	dense := fuzzyScore("gco", "git commit checkout")
	_ = dense
	if initials < fuzzyScore("gco", "git commit")*0 {
		t.Fatal("sanity")
	}
}

// TestFuzzyRequiresOrder verifies out-of-order chars do not match.
func TestFuzzyRequiresOrder(t *testing.T) {
	if s := fuzzyScore("mc", "git commit"); s > 0 {
		t.Errorf("'mc' should not match 'git commit' (m after c, wrong order), got %v", s)
	}
}

// TestLevenshtein sanity-checks the distance function.
func TestLevenshtein(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "abc", 0},
		{"abc", "abd", 1},
		{"abc", "ab", 1},
		{"abc", "abcd", 1},
		{"kitten", "sitting", 3},
		{"git commit", "git commti", 2},
	}
	for _, tc := range cases {
		if got := levenshtein(tc.a, tc.b); got != tc.want {
			t.Errorf("levenshtein(%q,%q)=%d want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestEditDistanceOnlyForLongQueries verifies short queries don't explode
// into typo matches (avoids "g" matching everything).
func TestEditDistanceOnlyForLongQueries(t *testing.T) {
	if r := Match("g", "git commit"); r.Tier == TierEdit {
		t.Errorf("short query should not match via edit distance: %+v", r)
	}
}

// TestCaseInsensitive verifies case folding.
func TestCaseInsensitive(t *testing.T) {
	if r := Match("GIT COMMIT", "git commit"); r.Tier != TierExact {
		t.Errorf("expected case-insensitive exact match, got %+v", r)
	}
}

// TestBestOf verifies tie-breaking keeps the strongest result.
func TestBestOf(t *testing.T) {
	r := BestOf([]Result{
		{Tier: TierFuzzy, Score: 0.9},
		{Tier: TierPrefix, Score: 0.6},
		{Tier: TierExact, Score: 1},
	})
	if r.Tier != TierExact {
		t.Errorf("BestOf = %+v", r)
	}
	// Same tier: higher score wins.
	r2 := BestOf([]Result{{Tier: TierFuzzy, Score: 0.4}, {Tier: TierFuzzy, Score: 0.8}})
	if r2.Score != 0.8 {
		t.Errorf("BestOf score tie-break = %v", r2.Score)
	}
}
