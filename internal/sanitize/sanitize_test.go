package sanitize

import "testing"

// TestSensitiveLines enumerates realistic secret-carrying lines.
func TestSensitiveLines(t *testing.T) {
	bad := []string{
		"export API_KEY=FAKE_SK_PREFIX_VALUE_abc123def456",
		"set API_KEY=FAKE_OPENAI_KEY_VALUE_xxxxxxxxxxxxxxxxxxxx",
		"curl -H \"Authorization: Bearer eyJhbGciOiJIUzI1NiJ9\" https://api",
		"aws configure --secret-value FAKE_AWS_KEY_VALUE",
		"mysql -u root --password=hunter2",
		"git push https://FAKE_GITHUB_PAT_VALUE@github.com/x/y.git",
		"gh auth login --with-token <<< FAKE_GHP_TOKEN_VALUE_0000000000000000000000",
		"ssh -i private_key user@host",
		"openssl genrsa -out /tmp/-----BEGIN RSA PRIVATE KEY-----",
		"export CLIENT_SECRET=s3cr3t",
		"echo slack-app-token-FAKEVALUE-1234567890",
		"Set-Cookie: session=abc123; HttpOnly",
		"connString=Server=db;Password=Pa$$w0rd",
		"az account get-access-token",
		"kubectl create secret generic db-credentials --from-literal=password=xxx",
	}
	for _, l := range bad {
		if !IsSensitive(l) {
			t.Errorf("expected sensitive: %q", l)
		}
	}
}

// TestBenignLines verifies ordinary developer commands pass through.
func TestBenignLines(t *testing.T) {
	good := []string{
		"git status",
		"git commit -m \"fix: resolve tokenization edge case\"",
		"npm run build",
		"docker compose up -d",
		"cd /home/user/project",
		"pip install requests",
		"kubectl get pods -n default",
		"curl -s https://api.github.com/repos/qiutuan/CmdPilot",
		"go test ./... -cover",
		"jq '.items[].name' data.json",
	}
	for _, l := range good {
		if IsSensitive(l) {
			t.Errorf("expected benign: %q", l)
		}
	}
}

// TestSanitizePreservesOrderAndDropsSensitive verifies the filter contract.
func TestSanitizePreservesOrderAndDropsSensitive(t *testing.T) {
	in := []string{
		"git status",
		"export API_KEY=sk-abc123",
		"git add -A",
		"ssh -i private_key.pem user@host",
		"git commit -m \"wip\"",
	}
	out := Sanitize(in)
	want := []string{"git status", "git add -A", "git commit -m \"wip\""}
	if len(out) != len(want) {
		t.Fatalf("Sanitize len = %d, want %d: %v", len(out), len(want), out)
	}
	for i := range want {
		if out[i] != want[i] {
			t.Fatalf("Sanitize[%d] = %q, want %q (full: %v)", i, out[i], want[i], out)
		}
	}
}

// TestSanitizeEmptyAndNil handles degenerate inputs.
func TestSanitizeEmptyAndNil(t *testing.T) {
	if out := Sanitize(nil); out != nil {
		t.Errorf("Sanitize(nil) = %v", out)
	}
	if out := Sanitize([]string{}); len(out) != 0 {
		t.Errorf("Sanitize(empty) = %v", out)
	}
}

// TestCaseInsensitivePatterns verifies uppercase/leetspeak variants are caught.
func TestCaseInsensitivePatterns(t *testing.T) {
	for _, l := range []string{"SET API_KEY=abc", "export PASSWORD=x", "Bearer abc123", "TOKEN: yyy"} {
		if !IsSensitive(l) {
			t.Errorf("expected sensitive (case variant): %q", l)
		}
	}
}
