package agent

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"go.uber.org/zap"
)

const (
	// DefaultMaxOutputBytes is the maximum output size before truncation.
	DefaultMaxOutputBytes int64 = 1 << 20 // 1MB

	// DefaultTerminateTimeout is how long to wait for graceful shutdown.
	DefaultTerminateTimeout = 5 * time.Second
)

// ProcessManager handles process lifecycle management.
type ProcessManager struct {
	logger *zap.Logger
}

// NewProcessManager creates a new process manager.
func NewProcessManager(logger *zap.Logger) *ProcessManager {
	return &ProcessManager{logger: logger}
}

// TerminateProcessTree sends SIGTERM, waits for timeout, then SIGKILL.
// Platform-specific implementation in process_manager_unix.go / process_manager_windows.go.
func (pm *ProcessManager) TerminateProcessTree(cmd *exec.Cmd, timeout time.Duration) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if timeout <= 0 {
		timeout = DefaultTerminateTimeout
	}
	return pm.terminateProcessTreeImpl(cmd, timeout)
}

// TruncateOutput truncates output keeping head + tail with a marker.
func (pm *ProcessManager) TruncateOutput(output string, maxBytes int64) (string, bool) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxOutputBytes
	}
	if int64(len(output)) <= maxBytes {
		return output, false
	}

	// Keep 40% head, 40% tail, 20% for marker
	headSize := int(maxBytes * 40 / 100)
	tailSize := int(maxBytes * 40 / 100)
	truncated := int64(len(output)) - maxBytes

	head := output[:headSize]
	tail := output[len(output)-tailSize:]

	marker := fmt.Sprintf("\n\n[... truncated %s (%d bytes) ...]\n\n",
		formatBytes(truncated), truncated)

	return head + marker + tail, true
}

func formatBytes(b int64) string {
	switch {
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%d bytes", b)
	}
}

// EnhancedExecutionResult extends ExecutionResult with truncation info.
type EnhancedExecutionResult struct {
	Command      string
	Output       string
	Error        string
	ExitCode     int
	Duration     time.Duration
	WasKilled    bool
	WasTruncated bool
	OriginalSize int64
	Severity     string
}

// BuildSuggestions returns alternative commands based on the denied command.
func BuildSuggestions(command string) []string {
	var suggestions []string
	lower := strings.ToLower(command)

	if strings.Contains(lower, "rm -rf") {
		suggestions = append(suggestions, "Use 'rm -ri' for interactive deletion")
		suggestions = append(suggestions, "Use 'trash' or 'trash-put' for safe deletion")
	}
	if strings.Contains(lower, "chmod -R 777") {
		suggestions = append(suggestions, "Use 'chmod -R 755' for directories, 'chmod -R 644' for files")
	}
	if strings.Contains(lower, "curl") && strings.Contains(lower, "| sh") {
		suggestions = append(suggestions, "Download the script first, review it, then execute")
	}

	return suggestions
}
