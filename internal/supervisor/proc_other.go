//go:build !unix

package supervisor

import "os/exec"

// Non-unix platforms are only supported for development; there is no
// graceful SIGTERM, so stop means kill.
func setProcAttr(*exec.Cmd) {}

func terminate(cmd *exec.Cmd) error { return cmd.Process.Kill() }

func cleanupGroup(int) {}
