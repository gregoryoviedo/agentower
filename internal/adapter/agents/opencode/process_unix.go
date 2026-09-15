//go:build !windows

package agents_opencode

import (
	"os/exec"
	"syscall"
)

// setProcessGroup places the child in its own process group so the
// manager can signal the whole tree (opencode serve spawns helpers).
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminateProcess asks the process group to shut down gracefully with
// SIGTERM, falling back to killing the direct child when the group is
// not addressable (e.g. the process already exited or was reparented).
func terminateProcess(cmd *exec.Cmd) {
	if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil {
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
}
