//go:build !windows

package cli

import (
	"os/exec"
	"syscall"
)

// startDetached spawns a process detached from the session (setsid).
func startDetached(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd
}
