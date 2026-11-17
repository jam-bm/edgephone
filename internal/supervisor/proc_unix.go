//go:build unix

package supervisor

import (
	"os/exec"
	"syscall"
)

// setProcAttr puts the service in its own process group so that stopping
// it also stops anything it spawned (shell wrappers, workers, ...).
func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminate(cmd *exec.Cmd) error {
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
}

func cleanupGroup(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}
