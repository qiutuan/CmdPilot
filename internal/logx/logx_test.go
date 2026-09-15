package logx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRotate verifies size-based rotation and the keep-count pruning.
func TestRotate(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir, "test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer l.Close()
	l.maxSize = 256 // small cap to force rotation quickly but larger than one line

	for i := 0; i < 500; i++ {
		l.Infof("line number %d with some padding to grow the file", i)
	}

	files := ListFiles(dir, "test")
	if len(files) == 0 {
		t.Fatal("no log files produced")
	}
	// keep = 3 means at most 3 rotated files + current = 4 files.
	if len(files) > 4 {
		t.Fatalf("too many log files: %d", len(files))
	}
	// Ensure current file is small again after rotation (≤ maxSize).
	st, err := os.Stat(filepath.Join(dir, "test.log"))
	if err != nil {
		t.Fatalf("stat current log: %v", err)
	}
	if st.Size() > 256 {
		t.Fatalf("current log not rotated, size=%d", st.Size())
	}
}

// TestLevelFilter verifies that debug lines are dropped at info level.
func TestLevelFilter(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir, "filter")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer l.Close()
	l.SetLevel(LevelInfo)
	l.Debugf("secret-debug")
	l.Infof("visible-info")
	recent := l.Recent(100)
	for _, line := range recent {
		if strings.Contains(line, "secret-debug") {
			t.Fatalf("debug line leaked at info level: %s", line)
		}
	}
	if len(recent) == 0 || !strings.Contains(recent[len(recent)-1], "visible-info") {
		t.Fatalf("info line missing from ring: %v", recent)
	}
}

// TestParseLevel covers all valid names and unknown fallback.
func TestParseLevel(t *testing.T) {
	cases := map[string]Level{"debug": LevelDebug, "INFO": LevelInfo, "warn": LevelWarn, "error": LevelError, "bogus": LevelInfo}
	for in, want := range cases {
		if got := ParseLevel(in); got != want {
			t.Errorf("ParseLevel(%q)=%v want %v", in, got, want)
		}
	}
}
