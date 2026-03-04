//go:build !windows

package agent

import (
	"os/exec"
	"syscall"
	"time"
)

func (pm *ProcessManager) terminateProcessTreeImpl(cmd *exec.Cmd, timeout time.Duration) error {
	pid := cmd.Process.Pid

	// Send SIGTERM to the process group (negative PID)
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		// Process might already be dead
		pm.logger.Debug("SIGTERM failed, process may have already exited")
	}

	// Wait for graceful shutdown
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case <-done:
		return nil // Process exited gracefully
	case <-time.After(timeout):
		pm.logger.Warn("process did not exit after SIGTERM, sending SIGKILL")
		// Force kill the process group
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		<-done // Wait for it to actually die
		return nil
	}
}

// SetProcessGroup configures the command to create a new process group.
// Must be called before cmd.Start().
func SetProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}
