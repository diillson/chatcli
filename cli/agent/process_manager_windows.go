//go:build windows

package agent

import (
	"fmt"
	"os/exec"
	"strconv"
	"time"
)

func (pm *ProcessManager) terminateProcessTreeImpl(cmd *exec.Cmd, timeout time.Duration) error {
	pid := cmd.Process.Pid

	// On Windows, use taskkill /T (tree kill) with /F (force) after timeout
	// First try graceful termination
	graceful := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T")
	if err := graceful.Run(); err != nil {
		pm.logger.Debug("graceful taskkill failed, process may have already exited")
	}

	// Wait for graceful shutdown
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case <-done:
		return nil
	case <-time.After(timeout):
		pm.logger.Warn("process did not exit after taskkill, using /F flag")
		force := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F")
		if err := force.Run(); err != nil {
			return fmt.Errorf("force taskkill failed: %w", err)
		}
		<-done
		return nil
	}
}

// SetProcessGroup is a no-op on Windows (process groups work differently).
func SetProcessGroup(cmd *exec.Cmd) {
	// On Windows, CREATE_NEW_PROCESS_GROUP is handled differently.
	// taskkill /T handles the tree kill.
}
