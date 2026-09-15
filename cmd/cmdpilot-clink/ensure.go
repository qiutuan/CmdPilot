package main

import (
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/qiutuan/CmdPilot/internal/client"
	"github.com/qiutuan/CmdPilot/internal/daemonstate"
)

// daemonClient builds a client from the daemon state file, or nil.
func daemonClient() *client.Client {
	st, err := daemonstate.Read()
	if err != nil || st == nil || st.Port == 0 {
		return nil
	}
	return client.New(fmt.Sprintf("http://127.0.0.1:%d", st.Port), st.Token)
}

// runEnsure spawns `cmdpilot daemon ensure` as a subprocess and waits.
// Kept in a separate file so the companion stays tiny.
func runEnsure() int {
	exe, err := os.Executable()
	if err != nil {
		return 1
	}
	// The companion lives next to cmdpilot(.exe); use that binary.
	dir := dirOf(exe)
	mainBin := os.Getenv("CMDPILOT_BIN")
	if mainBin == "" {
		candidates := []string{
			dir + sep() + "cmdpilot" + exeExt(),
			dir + sep() + ".." + sep() + "cmdpilot" + exeExt(),
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				mainBin = c
				break
			}
		}
	}
	if mainBin == "" {
		mainBin = "cmdpilot" // rely on PATH
	}
	cmd := exec.Command(mainBin, "daemon", "ensure")
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return 1
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
		return 0
	case <-time.After(8 * time.Second):
		_ = cmd.Process.Kill()
		return 1
	}
}

// healthCheck builds a client and probes; used after ensure.
func healthCheck() bool {
	c := daemonClient()
	if c == nil {
		return false
	}
	ok, _ := c.Health()
	return ok
}

// ensureHealth probes until healthy or timeout.
func ensureHealth(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if healthCheck() {
			return true
		}
		time.Sleep(150 * time.Millisecond)
	}
	return false
}

// small platform helpers ------------------------------------------------

func sep() string {
	if os.PathSeparator == '\\' {
		return "\\"
	}
	return "/"
}

func exeExt() string {
	if os.PathSeparator == '\\' {
		return ".exe"
	}
	return ""
}

func dirOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[:i]
		}
	}
	return "."
}
