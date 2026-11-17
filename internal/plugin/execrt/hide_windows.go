package execrt

import (
	"os/exec"
	"syscall"
)

// hideWindow keeps a console program started from the window from flashing a console.
func hideWindow(cmd *exec.Cmd) {
	const createNoWindow = 0x08000000
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}
