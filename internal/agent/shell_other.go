//go:build !unix

package agent

import "os/exec"

// setProcessGroup is a no-op off Unix; process-group semantics differ and
// cmd.WaitDelay is the backstop that keeps the timeout honest.
func setProcessGroup(*exec.Cmd) {}

func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
