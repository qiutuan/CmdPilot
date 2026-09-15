package version

import (
	"strings"
	"testing"
)

func TestString(t *testing.T) {
	s := String()
	if !strings.HasPrefix(s, "CmdPilot ") {
		t.Fatalf("String() = %q", s)
	}
	for _, part := range []string{Version, Commit, BuildDate} {
		if !strings.Contains(s, part) {
			t.Errorf("String() missing %q: %q", part, s)
		}
	}
	// 保证版本可被 -ldflags 覆盖且 String 仍有效
	oldV, oldC, oldD := Version, Commit, BuildDate
	Version, Commit, BuildDate = "9.9.9", "abc123", "2026-01-01"
	s2 := String()
	if !strings.Contains(s2, "9.9.9") || !strings.Contains(s2, "abc123") {
		t.Errorf("ldflags override not reflected: %q", s2)
	}
	Version, Commit, BuildDate = oldV, oldC, oldD
}
