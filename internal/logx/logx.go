// Package logx provides a minimal rotating file logger.
//
// Design goals: strictly local (no network), tiny footprint, crash-safe.
// Rotation policy: single file capped at maxSize bytes, keeping `keep` rotated files.
// A memory ring keeps the last N lines so `self-check` can report recent activity.
package logx

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Level is the log severity level.
type Level int

// Supported log levels, ordered by increasing severity.
const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

// String returns the lowercase name of the level.
func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "debug"
	case LevelInfo:
		return "info"
	case LevelWarn:
		return "warn"
	case LevelError:
		return "error"
	}
	return "unknown"
}

// ParseLevel converts a string to a Level; defaults to Info on unknown input.
func ParseLevel(s string) Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return LevelDebug
	case "warn", "warning":
		return LevelWarn
	case "error":
		return LevelError
	}
	return LevelInfo
}

// Defaults for the rotating logger.
const (
	DefaultMaxSize = 10 << 20 // 10 MB per file
	DefaultKeep    = 3        // keep 3 rotated files
	RingCapacity   = 200      // in-memory ring of recent lines
)

// Logger is a concurrency-safe rotating file logger.
type Logger struct {
	mu      sync.Mutex
	dir     string
	prefix  string
	level   Level
	maxSize int64
	keep    int
	file    *os.File
	size    int64
	ring    []string // recent lines, oldest first
}

// New creates a Logger writing to dir with the given file prefix.
// The directory is created if missing. A nil error means the logger is usable.
func New(dir, prefix string) (*Logger, error) {
	l := &Logger{dir: dir, prefix: prefix, level: LevelInfo, maxSize: DefaultMaxSize, keep: DefaultKeep}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("logx: create log dir: %w", err)
	}
	if err := l.open(); err != nil {
		return nil, err
	}
	return l, nil
}

// SetLevel changes the minimum severity written to disk.
func (l *Logger) SetLevel(lv Level) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.level = lv
}

// Level returns the current minimum severity.
func (l *Logger) Level() Level {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.level
}

// open opens (or re-opens) today's log file in append mode.
func (l *Logger) open() error {
	if l.file != nil {
		l.file.Close()
		l.file = nil
	}
	path := filepath.Join(l.dir, l.prefix+".log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("logx: open log file: %w", err)
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("logx: stat log file: %w", err)
	}
	l.file = f
	l.size = st.Size()
	return nil
}

// rotate renames the current file to prefix.N.log and prunes old ones.
// The handle is closed before renaming: Windows cannot rename an open file.
func (l *Logger) rotate() error {
	if l.file != nil {
		if err := l.file.Close(); err != nil {
			return fmt.Errorf("logx: close before rotate: %w", err)
		}
		l.file = nil
	}
	base := filepath.Join(l.dir, l.prefix)
	// Shift existing rotated files: keep-1 -> keep, ..., 1 -> 2, current -> 1.
	for i := l.keep - 1; i >= 1; i-- {
		old := fmt.Sprintf("%s.%d.log", base, i)
		next := fmt.Sprintf("%s.%d.log", base, i+1)
		os.Rename(old, next) //nolint:errcheck // best-effort pruning
	}
	cur := base + ".log"
	if err := os.Rename(cur, base+".1.log"); err != nil {
		// Roll back so logging continues against the still-present current file.
		_ = l.open()
		return fmt.Errorf("logx: rotate: %w", err)
	}
	return l.open()
}

// Log writes one formatted line at the given level.
func (l *Logger) Log(lv Level, format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if lv < l.level {
		return
	}
	msg := fmt.Sprintf(format, args...)
	ts := time.Now().Format("2006-01-02T15:04:05.000Z07:00")
	line := fmt.Sprintf("%s [%s] %s\n", ts, lv, msg)
	l.ring = append(l.ring, strings.TrimSuffix(line, "\n"))
	if len(l.ring) > RingCapacity {
		l.ring = l.ring[len(l.ring)-RingCapacity:]
	}
	if l.file == nil {
		return
	}
	if l.size+int64(len(line)) > l.maxSize {
		if err := l.rotate(); err != nil {
			return // keep logging in-memory only
		}
	}
	n, err := l.file.WriteString(line)
	if err != nil {
		return
	}
	l.size += int64(n)
}

// Debugf writes a debug-level line.
func (l *Logger) Debugf(format string, args ...any) { l.Log(LevelDebug, format, args...) }

// Infof writes an info-level line.
func (l *Logger) Infof(format string, args ...any) { l.Log(LevelInfo, format, args...) }

// Warnf writes a warning-level line.
func (l *Logger) Warnf(format string, args ...any) { l.Log(LevelWarn, format, args...) }

// Errorf writes an error-level line.
func (l *Logger) Errorf(format string, args ...any) { l.Log(LevelError, format, args...) }

// Recent returns the most recent ring lines (oldest first), capped at n.
func (l *Logger) Recent(n int) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n <= 0 || n > len(l.ring) {
		n = len(l.ring)
	}
	out := make([]string, n)
	copy(out, l.ring[len(l.ring)-n:])
	return out
}

// Close flushes and closes the underlying file.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		err := l.file.Close()
		l.file = nil
		return err
	}
	return nil
}

// ListFiles returns existing log file paths sorted by name (for CLI/self-check).
func ListFiles(dir, prefix string) []string {
	matches, err := filepath.Glob(filepath.Join(dir, prefix+"*.log"))
	if err != nil {
		return nil
	}
	sort.Strings(matches)
	return matches
}
