//go:build !windows

package execrt

import "os/exec"

func hideWindow(*exec.Cmd) {}
