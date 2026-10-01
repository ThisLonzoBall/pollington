//go:build unix

package agent

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the shell in its own process group so that killGroup can
// take down everything it spawned.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup kills the shell and all of its descendants.
//
// Killing only the direct child is not enough: `sh -c "sleep 30"` forks, and the
// grandchild inherits the output pipe. CombinedOutput waits for that pipe to
// close, so it blocks for the full 30 seconds even though the shell is dead -
// which defeats the timeout entirely. The negative PID targets the group.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
