// Package secrets protects sensitive values (e.g. the AI API key) at rest.
//
// On Windows the value is encrypted with DPAPI (CurrentUser scope, app-bound
// entropy) so only the same Windows user can decrypt it. On non-Windows
// platforms (used only for development/testing) values are stored with a
// clearly-marked "dev:" prefix and are NOT cryptographically protected;
// the CLI prints a warning in that case.
package secrets

import "fmt"

// ErrUnsupported marks a platform that cannot protect values securely.
var ErrUnsupported = fmt.Errorf("secrets: secure storage not supported on this platform")
