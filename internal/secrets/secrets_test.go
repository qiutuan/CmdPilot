package secrets

import "testing"

// TestRoundTrip verifies Protect/Unprotect round-trip on the current platform
// (dev encoding on non-Windows, DPAPI on Windows CI).
func TestRoundTrip(t *testing.T) {
	cases := []string{"", "sk-test-abc123", "openai-very-long-key-0123456789abcdef"}
	for _, in := range cases {
		enc, err := Protect(in)
		if err != nil {
			t.Fatalf("Protect(%q): %v", in, err)
		}
		out, err := Unprotect(enc)
		if err != nil {
			t.Fatalf("Unprotect: %v", err)
		}
		if out != in {
			t.Fatalf("round trip mismatch: got %q want %q", out, in)
		}
	}
}

// TestUnprotectGarbage verifies invalid payloads produce an error, never panic.
func TestUnprotectGarbage(t *testing.T) {
	for _, bad := range []string{"!!!not-base64!!!", "plain-without-prefix"} {
		if _, err := Unprotect(bad); err == nil {
			t.Errorf("Unprotect(%q) should fail", bad)
		}
	}
}

// TestTwoDistinctValuesDoNotCollide guards against accidentally identical blobs.
func TestTwoDistinctValuesDoNotCollide(t *testing.T) {
	a, _ := Protect("value-A")
	b, _ := Protect("value-B")
	if a == "" || a == b {
		t.Fatalf("distinct values must produce distinct payloads (a=%q b=%q)", a, b)
	}
}
