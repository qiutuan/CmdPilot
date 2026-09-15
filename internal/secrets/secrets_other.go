//go:build !windows

package secrets

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// devPrefix marks values that are NOT cryptographically protected (dev-only).
const devPrefix = "dev:"

// Protect stores plaintext with a visible dev marker (no real protection).
// Used only for development/testing on non-Windows platforms.
func Protect(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	return devPrefix + base64.StdEncoding.EncodeToString([]byte(plaintext)), nil
}

// Unprotect decodes a dev-marked value produced by Protect.
func Unprotect(encoded string) (string, error) {
	if encoded == "" {
		return "", nil
	}
	if !strings.HasPrefix(encoded, devPrefix) {
		return "", ErrUnsupported
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(encoded, devPrefix))
	if err != nil {
		return "", fmt.Errorf("secrets: invalid dev payload: %w", err)
	}
	return string(raw), nil
}
