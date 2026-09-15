// Package daemonstate manages the daemon discovery file (port + auth token),
// enabling adapters/CLI to find and talk to the running CmdPilot daemon.
package daemonstate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/qiutuan/CmdPilot/internal/config"
)

// State describes a running daemon.
type State struct {
	PID       int    `json:"pid"`
	Port      int    `json:"port"`
	Token     string `json:"token"`
	StartedAt string `json:"started_at"`
}

// Path returns the daemon state file location.
func Path() string { return filepath.Join(config.BaseDir(), "daemon.json") }

// Write persists the state file with restrictive permissions.
func Write(s State) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("daemonstate: marshal: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(Path()), 0o700); err != nil {
		return fmt.Errorf("daemonstate: mkdir: %w", err)
	}
	tmp := Path() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("daemonstate: write: %w", err)
	}
	return os.Rename(tmp, Path())
}

// Read loads the state file; returns (nil, nil) when absent.
func Read() (*State, error) {
	data, err := os.ReadFile(Path())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("daemonstate: read: %w", err)
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("daemonstate: parse: %w", err)
	}
	return &s, nil
}

// Remove deletes the state file.
func Remove() { _ = os.Remove(Path()) } //nolint:errcheck // best-effort

// writeFile exists so tests can corrupt the state file.
func writeFile(path string, data []byte) error { return os.WriteFile(path, data, 0o600) }

// WaitHealthy polls check until it returns true or the timeout elapses.
func WaitHealthy(check func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}
