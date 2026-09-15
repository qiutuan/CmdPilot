package knowledge

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// loadSources parses the three source files for cross-checking against the gz.
func loadSources(t *testing.T) []Command {
	t.Helper()
	var out []Command
	for _, f := range []string{"data/cmd.json", "data/ps.json", "data/external.json"} {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		var part struct {
			Commands []Command `json:"commands"`
		}
		if err := json.Unmarshal(raw, &part); err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		out = append(out, part.Commands...)
	}
	return out
}

// TestCounts enforces the M1 minimum catalog sizes.
func TestCounts(t *testing.T) {
	db, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := len(db.ByShell(ShellCmd)); got < 150 {
		t.Errorf("CMD commands = %d, want >= 150", got)
	}
	if got := len(db.ByShell(ShellPS)); got < 200 {
		t.Errorf("PowerShell cmdlets = %d, want >= 200", got)
	}
	if got := len(db.ByShell(ShellExternal)); got < 50 {
		t.Errorf("external templates = %d, want >= 50", got)
	}
}

// TestNoDuplicates ensures command names are unique across the whole base.
func TestNoDuplicates(t *testing.T) {
	db, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, c := range db.Commands {
		if seen[c.Name] {
			t.Errorf("duplicate command %q", c.Name)
		}
		seen[c.Name] = true
	}
}

// TestFieldsComplete verifies every entry carries the mandatory metadata.
func TestFieldsComplete(t *testing.T) {
	db, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range db.Commands {
		if c.Desc == "" {
			t.Errorf("%s: empty desc", c.Name)
		}
		if c.Shell != ShellCmd && c.Shell != ShellPS && c.Shell != ShellExternal {
			t.Errorf("%s: invalid shell %q", c.Name, c.Shell)
		}
		if len(c.Tags) == 0 {
			t.Errorf("%s: no tags", c.Name)
		}
		if len(c.Examples) == 0 {
			t.Errorf("%s: no examples", c.Name)
		}
	}
}

// TestGzMatchesSources guards against drift between sources and the embedded blob.
func TestGzMatchesSources(t *testing.T) {
	db, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	src := loadSources(t)
	if len(db.Commands) != len(src) {
		t.Fatalf("embedded %d commands but sources have %d", len(db.Commands), len(src))
	}
	byName := map[string]Command{}
	for _, c := range db.Commands {
		byName[c.Name] = c
	}
	for _, s := range src {
		e, ok := byName[s.Name]
		if !ok {
			t.Fatalf("source command %q missing from embedded blob", s.Name)
		}
		if e.Desc != s.Desc || e.Shell != s.Shell || strings.Join(e.Params, ",") != strings.Join(s.Params, ",") {
			t.Errorf("embedded %q differs from source", s.Name)
		}
	}
}

// TestFindAndAll sanity-checks lookup helpers.
func TestFindAndAll(t *testing.T) {
	db, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if db.Find("git commit") == nil {
		t.Error("Find(git commit) = nil")
	}
	if db.Find("no-such-command-xyz") != nil {
		t.Error("Find should return nil for unknown command")
	}
	if len(db.All()) != db.Count() {
		t.Error("All() length mismatch")
	}
}

// TestVersion sanity-checks schema version.
func TestVersion(t *testing.T) {
	db, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if db.Version <= 0 {
		t.Errorf("invalid schema version %d", db.Version)
	}
}
