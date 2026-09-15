package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/qiutuan/CmdPilot/internal/client"
)

// tempDir creates a private temp dir for eval artifacts.
func tempDir(prefix string) (string, error) {
	return os.MkdirTemp("", "cmdpilot-"+prefix+"-*")
}

func removeTemp(dir string) {
	if dir != "" {
		_ = os.RemoveAll(dir)
	}
}

// projectRoot returns the repo root (dir containing go.mod).
func projectRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	cur := wd
	for {
		if _, err := os.Stat(filepath.Join(cur, "go.mod")); err == nil {
			return cur, nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", os.ErrNotExist
		}
		cur = parent
	}
}

// setBaseDir makes CmdPilot use base as its data root for this process and all
// children spawned afterwards. Linux: $XDG_DATA_HOME/cmdpilot.
func setBaseDir(base string) {
	_ = os.Setenv("XDG_DATA_HOME", base)
	_ = os.Setenv("LOCALAPPDATA", "")
}

// daemonDataDir returns base/cmdpilot (the effective data dir).
func daemonDataDir(base string) string {
	return filepath.Join(base, "cmdpilot")
}

// buildDaemon builds the daemon binary into dir and returns its path.
func buildDaemon(dir string) (string, error) {
	root, err := projectRoot()
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "cmdpilot")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/cmdpilot")
	cmd.Dir = root
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return bin, nil
}

// startDaemon ensures the daemon runs against base (env XDG_DATA_HOME=base).
func startDaemon(bin, base string) (*client.Client, string, error) {
	cmd := exec.Command(bin, "daemon", "ensure")
	cmd.Env = append(os.Environ(), "XDG_DATA_HOME="+base)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, "", fmt.Errorf("daemon ensure: %v (%s)", err, out)
	}
	data := daemonDataDir(base)
	raw, err := os.ReadFile(filepath.Join(data, "daemon.json"))
	if err != nil {
		return nil, "", fmt.Errorf("daemon.json: %v", err)
	}
	var st struct {
		Port  int    `json:"Port"`
		Token string `json:"Token"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, "", err
	}
	url := fmt.Sprintf("http://127.0.0.1:%d", st.Port)
	c := client.New(url, st.Token)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if ok, _ := c.Health(); ok {
			return c, st.Token, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, "", fmt.Errorf("daemon not healthy in 8s")
}

// stopDaemon shuts the daemon down via HTTP (best-effort).
func stopDaemon(c *client.Client) {
	if c != nil {
		_ = c.Shutdown()
	}
}
