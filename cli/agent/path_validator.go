package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/diillson/chatcli/pkg/fspath"
	"go.uber.org/zap"
)

// PathValidator validates file paths against workspace boundaries.
//
// Not wired into any execution path today: command validation runs through
// CommandValidator and the coder engine's own boundary check, and reads
// go through SensitiveReadPaths. It is kept because it is part of the
// package's exported surface, and it is kept correct — a security helper
// that is wrong is worse than one that is unused, and the next caller
// should not inherit the macOS denylist bug this comment replaced.
type PathValidator struct {
	workspaceBoundary string
	logger            *zap.Logger
}

// Sensitive system paths that should never be written to.
var sensitivePaths = []string{
	"/etc/passwd", "/etc/shadow", "/etc/sudoers",
	"/etc/ssh/", "/etc/ssl/",
	"/proc/", "/sys/", "/dev/",
	"/boot/", "/sbin/",
}

// resolvedSensitivePaths carries each entry above in both spellings: the
// literal one and the one it resolves to. The candidate path is compared
// after EvalSymlinks, and on macOS /etc resolves to /private/etc — so the
// literal entries matched nothing and the denylist was inert there.
var resolvedSensitivePaths = resolveDenyList(sensitivePaths)

func resolveDenyList(paths []string) []string {
	out := make([]string, 0, len(paths)*2)
	for _, p := range paths {
		out = append(out, p)
		trimmed := strings.TrimSuffix(p, "/")
		resolved, err := filepath.EvalSymlinks(trimmed)
		if err != nil || resolved == trimmed {
			continue
		}
		if strings.HasSuffix(p, "/") {
			resolved += "/"
		}
		out = append(out, resolved)
	}
	return out
}

// Allowed system binary paths (read/execute only).
var systemBinPaths = []string{
	"/usr/bin/", "/usr/local/bin/", "/bin/", "/usr/sbin/",
	"/opt/homebrew/bin/",
}

// Matches "..", delimited by either separator: Windows commands use "\"
// (e.g. "..\..\secret"), and a slash-only pattern would let those through.
var pathTraversalPattern = regexp.MustCompile(`(?:^|[/\\])\.\.(?:[/\\]|$)`)

// NewPathValidator creates a new path validator.
func NewPathValidator(workspace string, logger *zap.Logger) *PathValidator {
	return &PathValidator{
		workspaceBoundary: workspace,
		logger:            logger,
	}
}

// SetWorkspaceBoundary updates the workspace boundary.
func (pv *PathValidator) SetWorkspaceBoundary(path string) {
	if abs, err := filepath.Abs(path); err == nil {
		pv.workspaceBoundary = abs
	} else {
		pv.workspaceBoundary = path
	}
}

// DetectPathTraversal checks if a command contains path traversal attempts.
func (pv *PathValidator) DetectPathTraversal(command string) (bool, string) {
	// Check for ../ patterns
	if pathTraversalPattern.MatchString(command) {
		return true, "command contains path traversal (../)"
	}

	// Check for home directory escape via tilde
	if strings.Contains(command, "~root") {
		return true, "command attempts to access root home directory"
	}

	// Extract file paths from command and check each
	paths := extractPaths(command)
	for _, p := range paths {
		resolved, err := resolvePathSafe(p)
		if err != nil {
			continue
		}

		// Check sensitive paths
		for _, sensitive := range resolvedSensitivePaths {
			if strings.HasPrefix(resolved, sensitive) {
				return true, fmt.Sprintf("command accesses sensitive path: %s", sensitive)
			}
		}
		for _, sensitive := range fspath.WindowsSystemRoots() {
			if fspath.WithinBoundary(resolved, sensitive) {
				return true, fmt.Sprintf("command accesses sensitive path: %s", sensitive)
			}
		}
	}

	return false, ""
}

// IsWithinWorkspace checks if a path is within the workspace boundary.
func (pv *PathValidator) IsWithinWorkspace(targetPath string) bool {
	if pv.workspaceBoundary == "" {
		return true // No boundary set
	}

	resolved, err := resolvePathSafe(targetPath)
	if err != nil {
		return false
	}

	boundary, err := resolvePathSafe(pv.workspaceBoundary)
	if err != nil {
		return false
	}

	// Allow system binaries (read/execute)
	for _, binPath := range systemBinPaths {
		if strings.HasPrefix(resolved, binPath) {
			return true
		}
	}

	return fspath.WithinBoundary(resolved, boundary)
}

// ValidateFilePaths validates all paths in a command against safety rules.
func (pv *PathValidator) ValidateFilePaths(command string) ValidationResult {
	// Check for path traversal
	if traversal, reason := pv.DetectPathTraversal(command); traversal {
		return ValidationResult{
			Allowed:     false,
			Reason:      reason,
			Severity:    "high",
			Suggestions: []string{"Use absolute paths within the workspace"},
			MatchedRule: "path_traversal",
		}
	}

	// Check workspace boundary
	if pv.workspaceBoundary != "" {
		paths := extractPaths(command)
		for _, p := range paths {
			if !pv.IsWithinWorkspace(p) {
				return ValidationResult{
					Allowed:     false,
					Reason:      fmt.Sprintf("path %q is outside workspace boundary %q", p, pv.workspaceBoundary),
					Severity:    "medium",
					Suggestions: []string{"Operate within the project workspace"},
					MatchedRule: "workspace_boundary",
				}
			}
		}
	}

	return ValidationResult{Allowed: true}
}

// resolvePathSafe resolves a path without following symlinks outside workspace.
func resolvePathSafe(path string) (string, error) {
	// Clean the path first
	cleaned := filepath.Clean(path)

	// Try to evaluate symlinks
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		// If file doesn't exist yet, resolve the parent
		parent := filepath.Dir(cleaned)
		resolvedParent, err2 := filepath.EvalSymlinks(parent)
		if err2 != nil {
			// Use cleaned path as-is if parent also doesn't exist
			abs, err3 := filepath.Abs(cleaned)
			if err3 != nil {
				return cleaned, nil
			}
			return abs, nil
		}
		return filepath.Join(resolvedParent, filepath.Base(cleaned)), nil
	}

	return resolved, nil
}

// extractPaths extracts potential file paths from a command string.
func extractPaths(command string) []string {
	var paths []string
	// Split on spaces, respecting quotes
	parts := splitCommandArgs(command)
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// Skip flags
		if strings.HasPrefix(part, "-") {
			continue
		}
		// Check if it looks like a path
		if strings.Contains(part, "/") || strings.Contains(part, string(os.PathSeparator)) || strings.HasPrefix(part, ".") || strings.HasPrefix(part, "~") {
			paths = append(paths, part)
		}
	}
	return paths
}

// splitCommandArgs splits a command string respecting quotes.
func splitCommandArgs(command string) []string {
	var parts []string
	var current strings.Builder
	inSingle := false
	inDouble := false

	for _, r := range command {
		switch {
		case r == '\'' && !inDouble:
			inSingle = !inSingle
		case r == '"' && !inSingle:
			inDouble = !inDouble
		case r == ' ' && !inSingle && !inDouble:
			if current.Len() > 0 {
				parts = append(parts, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	return parts
}
