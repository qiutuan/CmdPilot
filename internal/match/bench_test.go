package match

import "testing"

// BenchmarkMatchPrefix benchmarks the hot path (prefix matching).
func BenchmarkMatchPrefix(b *testing.B) {
	for i := 0; i < b.N; i++ {
		Match("git c", "git commit")
	}
}

// BenchmarkMatchFuzzy benchmarks the fuzzy path.
func BenchmarkMatchFuzzy(b *testing.B) {
	for i := 0; i < b.N; i++ {
		Match("gco", "git checkout")
	}
}

// BenchmarkMatchEdit benchmarks the edit-distance path.
func BenchmarkMatchEdit(b *testing.B) {
	for i := 0; i < b.N; i++ {
		Match("git checkot", "git checkout")
	}
}

// BenchmarkMatchNoMatch benchmarks the reject path.
func BenchmarkMatchNoMatch(b *testing.B) {
	for i := 0; i < b.N; i++ {
		Match("zzzzqq", "git commit")
	}
}

// BenchmarkLevenshtein benchmarks the raw distance function.
func BenchmarkLevenshtein(b *testing.B) {
	for i := 0; i < b.N; i++ {
		levenshtein("git commit", "git commti")
	}
}
