// Package sanitize filters sensitive lines out of command history before they
// are sent to the AI provider. Lines containing likely secrets (keys, tokens,
// passwords, bearer credentials, ...) are dropped entirely; the rest keep
// their original order. Pure functions, fully unit-tested.
package sanitize

import "regexp"

// patterns match lines that plausibly carry secrets. Case-insensitive.
var patterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bapi[_-]?key\b\s*[=:]`),
	regexp.MustCompile(`(?i)\baccess[_-]?key\b`),
	regexp.MustCompile(`(?i)\bclient[_-]?secret\b`),
	regexp.MustCompile(`(?i)\bprivate[_-]?key\b`),
	regexp.MustCompile(`(?i)\bpassword\b`),
	regexp.MustCompile(`(?i)\bpasswd\b`),
	regexp.MustCompile(`(?i)\bpwd\b\s*[=:]`),
	regexp.MustCompile(`(?i)\bsecret\b`),
	regexp.MustCompile(`(?i)\btoken\b`),
	regexp.MustCompile(`(?i)\bbearer\b`),
	regexp.MustCompile(`(?i)\bauthorization\b`),
	regexp.MustCompile(`(?i)\bcookie\b`),
	regexp.MustCompile(`(?i)\bconnection[_-]?string\b`),
	regexp.MustCompile(`(?i)-----BEGIN [A-Z ]*PRIVATE KEY`),
	regexp.MustCompile(`(?i)\bsk-[A-Za-z0-9_\-]{8,}`),      // OpenAI-style keys
	regexp.MustCompile(`(?i)\bgithub_pat_[A-Za-z0-9_]+`),   // GitHub fine-grained PAT
	regexp.MustCompile(`(?i)\bgh[pousr]_[A-Za-z0-9]{20,}`), // GitHub classic tokens
	regexp.MustCompile(`(?i)\bAKIA[0-9A-Z]{16}`),           // AWS access key id
	regexp.MustCompile(`(?i)\baws_secret\b`),
	regexp.MustCompile(`(?i)\bxox[baprs]-[A-Za-z0-9\-]{10,}`), // Slack tokens
}

// IsSensitive reports whether a single history line should never leave the
// machine.
func IsSensitive(line string) bool {
	for _, re := range patterns {
		if re.MatchString(line) {
			return true
		}
	}
	return false
}

// Sanitize returns the subset of lines that is safe to forward to the AI,
// preserving order. Sensitive lines are removed, not redacted.
func Sanitize(lines []string) []string {
	if len(lines) == 0 {
		return nil
	}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if !IsSensitive(l) {
			out = append(out, l)
		}
	}
	return out
}
