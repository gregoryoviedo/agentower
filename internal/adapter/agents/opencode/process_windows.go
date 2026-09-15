//go:build windows

package agents_opencode

import (
	"os/exec"
	"strconv"
	"syscall"
)

// Windows process creation flags. CREATE_NEW_PROCESS_GROUP lets us
// deliver a Ctrl-Break to the tree later; CREATE_NO_WINDOW keeps the
// console from flashing when the bot runs under the tray wrapper.
const (
	createNewProcessGroup = 0x00000200
	createNoWindow        = 0x08000000
)

// setProcessGroup detaches the child into its own process group and
// hides the console window that a plain exec.Command would allocate.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNewProcessGroup | createNoWindow,
		HideWindow:    true,
	}
}

// terminateProcess kills the child and any grandchildren it spawned.
// Windows has no SIGTERM/process-group signal, so taskkill /T walks the
// tree; if taskkill is unavailable we fall back to killing the direct
// child.
func terminateProcess(cmd *exec.Cmd) {
	pid := cmd.Process.Pid
	kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid))
	kill.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow, HideWindow: true}
	if err := kill.Run(); err != nil {
		_ = cmd.Process.Kill()
	}
}
