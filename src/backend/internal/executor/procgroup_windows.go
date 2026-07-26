//go:build windows

package executor

import "os/exec"

// Windows has no POSIX process groups, so cancellation falls back to Go's
// default (kill the process we spawned). Rigger's server runs in a Linux
// container; this exists so the package still builds on a Windows dev box.
func setProcGroup(cmd *exec.Cmd) {
	cmd.WaitDelay = waitDelay
}
