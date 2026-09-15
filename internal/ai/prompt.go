package ai

import (
	"fmt"
	"strings"
)

// PromptInput carries everything needed to build the AI completion prompt.
type PromptInput struct {
	Shell    string   // cmd | ps
	CWD      string   // current working directory (may include marker if git repo)
	Input    string   // current input line (prefix to extend)
	History  []string // sanitized recent history (never contains secrets)
	LocalTop []string // top-5 local candidates used as few-shot reference
	GitRepo  bool     // whether CWD is inside a git repository
}

// SystemPrompt is the fixed system instruction.
const SystemPrompt = `You are CmdPilot, an expert Windows command-line completion assistant embedded in a terminal.
Given the user's current input, reply with ONLY the completion suffix that extends the input into a complete, correct, idiomatic command.
Rules:
- Return only the suffix (what comes after the current input). Never repeat the input itself.
- Do not add explanations, markdown, backticks, quotes, or trailing newlines.
- Respect the terminal type (cmd or powershell): use the correct syntax, flags and cmdlets.
- If the input is already complete, reply with exactly one space.
- If you are not confident, reply with one space.`

// BuildPrompt constructs the user message for the chat completion request.
// Pure function, unit-tested.
func BuildPrompt(in PromptInput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Terminal: %s\n", in.Shell)
	fmt.Fprintf(&b, "Working directory: %s", in.CWD)
	if in.GitRepo {
		b.WriteString(" (git repository)")
	}
	fmt.Fprintf(&b, "\nCurrent input: %q\n", in.Input)
	if len(in.History) > 0 {
		b.WriteString("Recent commands (for context):\n")
		for i, h := range in.History {
			fmt.Fprintf(&b, "%d. %s\n", i+1, h)
		}
	}
	if len(in.LocalTop) > 0 {
		b.WriteString("Local candidate suggestions (reference, ranked):\n")
		for i, c := range in.LocalTop {
			fmt.Fprintf(&b, "%d. %s\n", i+1, c)
		}
	}
	b.WriteString("Completion suffix (only the suffix):")
	return b.String()
}

// ValidateSuffix checks an AI-returned suffix against the input and returns a
// cleaned suffix, or "" if the result cannot be used. Pure function.
//
// Rules: no newlines (blocks multi-line injection), hard length cap, and no
// duplicate leading space when the input already ends with one.
func ValidateSuffix(input, suffix string) string {
	if strings.ContainsAny(suffix, "\r\n") {
		return "" // never allow multi-line injection (checked on raw input)
	}
	s := strings.TrimSpace(suffix)
	if s == "" {
		return ""
	}
	if len(s) > 512 {
		s = s[:512] // hard cap
	}
	if strings.HasSuffix(input, " ") && strings.HasPrefix(s, " ") {
		s = strings.TrimPrefix(s, " ")
	}
	return s
}
