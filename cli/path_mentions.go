package cli

import (
	"os"
	"regexp"
	"strings"
)

// knownAtCommands is the set of @ commands that should NOT be treated as path mentions.
var knownAtCommands = map[string]bool{
	"@file":      true,
	"@command":   true,
	"@coder":     true,
	"@websearch": true,
	"@webfetch":  true,
}

// pathMentionRe matches @path-like patterns:
// - @./relative/path
// - @src/file.go
// - @~/home/file
// - @/absolute/path
// Does NOT match @word (no path separators) or known commands.
var pathMentionRe = regexp.MustCompile(`@((?:\./|~/|/|[a-zA-Z0-9_-]+/)[\w./_~-]+)`)

// expandPathMentions converts @path/to/file patterns into @file path/to/file.
// This allows users to type @src/main.go directly instead of @file src/main.go.
// Only expands when the path actually exists on the filesystem.
func expandPathMentions(input string) string {
	return pathMentionRe.ReplaceAllStringFunc(input, func(match string) string {
		path := match[1:] // strip leading @

		// Skip known @ commands
		atWord := "@" + strings.SplitN(path, "/", 2)[0]
		if knownAtCommands[strings.ToLower(atWord)] {
			return match
		}

		// Expand ~ to home dir
		expandedPath := path
		if strings.HasPrefix(expandedPath, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				expandedPath = home + expandedPath[1:]
			}
		}

		// Verify the path exists (file or directory)
		if _, err := os.Stat(expandedPath); err != nil {
			return match // Path doesn't exist, leave as-is
		}

		// Convert to @file syntax
		return "@file " + path
	})
}
