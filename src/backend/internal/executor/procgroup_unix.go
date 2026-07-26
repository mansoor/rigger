//go:build !windows

package executor

import (
	"errors"
	"os/exec"
	"syscall"
)

// setProcGroup puts a cancellable command in its own process group and makes
// cancellation kill the whole group rather than just the process we spawned.
//
// This matters because `docker compose …` is not one process: the docker CLI
// execs /usr/libexec/docker/cli-plugins/docker-compose as a CHILD. Killing only
// the direct child — all exec.CommandContext does by default — leaves the plugin
// orphaned, reparented to init, and still streaming. For a following stream
// (`logs -f`, which never exits on its own) that means one leaked process per
// log view, accumulating for the lifetime of the container.
//
// Setpgid makes the child the leader of a NEW group, so kill(-pid) reaches the
// docker CLI and its plugin without ever touching the server's own group.
func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Negative pid ⇒ the whole process group.
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil // already gone; not a failure
		}
		return err
	}
	// Backstop: Wait also waits on the goroutines copying stdout/stderr, which
	// only finish when every holder of the pipe's write end is gone. Killing the
	// group should achieve that, but don't let a stray holder hang Wait forever.
	cmd.WaitDelay = waitDelay
}
