package workers

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/diillson/chatcli/cli/agent"
)

func init() {
	// When the session workspace is initialized (or torn down), swap our
	// overflow directory accordingly.
	agent.RegisterResultDirSetter(SetResultDir)
}

const (
	// MaxInlineResultBytes is the maximum size of a tool result that can be
	// included inline in the conversation history. Results larger than this
	// are persisted to a temporary file and replaced with a reference.
	// This prevents context window saturation from large outputs (e.g., full
	// file contents, verbose test output, large git diffs).
	//
	// Must be less than MaxWorkerOutputBytes (30KB) to ensure individual results
	// are truncated before the aggregate feedback truncation kicks in.
	MaxInlineResultBytes = 20 * 1024 // 20KB

	// TruncatedResultSuffix is appended when a result is stored to disk.
	TruncatedResultSuffix = "\n... [full output saved to %s — %d bytes total]"

	// InlinePreviewBytes controls how much of the result is kept inline
	// as a preview when the full result is stored to disk.
	InlinePreviewBytes = 4 * 1024 // 4KB preview
)

var (
	resultDir       string
	resultDirMu     sync.RWMutex
	resultDirOnce   sync.Once
	resultCounter   uint64
	resultDirCustom string // set by SetResultDir (session workspace)
)

// SetResultDir overrides the directory used for persisting large tool results.
// Called by the session workspace so overflow lands inside the session scratch
// area (which is on the read allowlist). Empty resets to default behaviour.
func SetResultDir(dir string) {
	resultDirMu.Lock()
	resultDirCustom = dir
	resultDirMu.Unlock()
}

// getResultDir returns (or creates) the temporary directory for large tool results.
// If SetResultDir was called with a non-empty path, that path wins; otherwise
// falls back to $TMPDIR/chatcli-tool-results (shared across sessions).
func getResultDir() string {
	resultDirMu.RLock()
	custom := resultDirCustom
	resultDirMu.RUnlock()
	if custom != "" {
		_ = os.MkdirAll(custom, 0o700)
		return custom
	}
	resultDirOnce.Do(func() {
		dir := filepath.Join(os.TempDir(), "chatcli-tool-results")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return
		}
		resultDir = dir
	})
	return resultDir
}

// TruncateToolResult checks if a tool result exceeds MaxInlineResultBytes.
// If so, it saves the full result to a temporary file and returns a truncated
// version with a reference to the file.
// If the result is within limits, it returns the original unchanged.
func TruncateToolResult(subcmd, result string) string {
	if len(result) <= MaxInlineResultBytes {
		return result
	}

	// Save full result to disk
	n := atomic.AddUint64(&resultCounter, 1)
	filename := fmt.Sprintf("result_%s_%d.txt", subcmd, n)
	fullPath := filepath.Join(getResultDir(), filename)

	if err := os.WriteFile(fullPath, []byte(result), 0o600); err != nil {
		// If we can't save, just truncate hard
		return result[:MaxInlineResultBytes] + "\n... [output truncated — write to disk failed]"
	}

	// Return preview + reference
	preview := result[:InlinePreviewBytes]
	// Try to cut at a newline boundary for cleaner output
	if lastNL := strings.LastIndex(preview, "\n"); lastNL > InlinePreviewBytes/2 {
		preview = preview[:lastNL+1]
	}

	return preview + fmt.Sprintf(TruncatedResultSuffix, fullPath, len(result))
}

// CleanupResultFiles removes all temporary result files.
// Called at the end of an agent session.
func CleanupResultFiles() {
	dir := getResultDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "result_") && strings.HasSuffix(e.Name(), ".txt") {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// ReadStoredResult reads a previously stored result file.
// Returns the content or an error message if the file is not found.
func ReadStoredResult(path string) string {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return fmt.Sprintf("[ERROR] Could not read stored result: %v", err)
	}
	return string(data)
}
