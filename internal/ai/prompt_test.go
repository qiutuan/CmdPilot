package ai

import (
	"strings"
	"testing"
)

// TestBuildPrompt verifies the prompt structure and content injection.
func TestBuildPrompt(t *testing.T) {
	p := BuildPrompt(PromptInput{
		Shell: "ps", CWD: "C:\\dev\\proj", Input: "git sta",
		History: []string{"git add .", "git commit -m wip"}, LocalTop: []string{"git status", "git stash"},
		GitRepo: true,
	})
	for _, want := range []string{"Terminal: ps", "C:\\dev\\proj", "(git repository)", `Current input: "git sta"`, "git add .", "git status", "Completion suffix"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q:\n%s", want, p)
		}
	}
}

// TestBuildPromptNoHistory verifies empty sections are omitted cleanly.
func TestBuildPromptNoHistory(t *testing.T) {
	p := BuildPrompt(PromptInput{Shell: "cmd", CWD: "C:\\", Input: "dir"})
	if strings.Contains(p, "Recent commands") {
		t.Errorf("empty history section should be omitted:\n%s", p)
	}
	if strings.Contains(p, "Local candidate") {
		t.Errorf("empty candidates section should be omitted:\n%s", p)
	}
}

// TestValidateSuffix covers accept/reject rules.
func TestValidateSuffix(t *testing.T) {
	cases := []struct {
		input, suffix string
		want          string
	}{
		{"git c", "ommit", "ommit"},
		{"git c", " ommit", "ommit"},        // leading space stripped
		{"git ", " add", "add"},             // no double space
		{"git ", "add", "add"},
		{"git c", "ommit\nrm -rf /", ""},    // newline injection blocked
		{"git c", "ommit\r", ""},            // CR blocked
		{"git c", "   ", ""},                // whitespace only
		{"git c", "", ""},
		{"git c", strings.Repeat("x", 600), strings.Repeat("x", 512)}, // capped
	}
	for _, tc := range cases {
		got := ValidateSuffix(tc.input, tc.suffix)
		if got != tc.want {
			t.Errorf("ValidateSuffix(%q,%q) = %q, want %q", tc.input, tc.suffix, got, tc.want)
		}
	}
}
