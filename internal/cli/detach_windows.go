//go:build windows

package cli

import (
	"os/exec"
	"syscall"
)

// startDetached spawns a process detached from the console (no window).
func startDetached(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | 0x08000000, // CREATE_NO_WINDOW
	}
	return cmd
}
