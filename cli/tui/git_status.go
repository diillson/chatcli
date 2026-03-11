package tui

import (
	"os/exec"
	"strconv"
	"strings"
)

// getGitModifiedFiles returns modified files with diff stats from git.
// Returns nil if not in a git repo or git is not available.
func getGitModifiedFiles(cwd string) []FileChange {
	// Get both staged and unstaged changes
	cmd := exec.Command("git", "diff", "--numstat", "HEAD")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		// Try without HEAD (for repos with no commits yet)
		cmd = exec.Command("git", "diff", "--numstat")
		cmd.Dir = cwd
		out, err = cmd.Output()
		if err != nil {
			return nil
		}
	}

	var files []FileChange
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 3 {
			continue
		}
		adds, _ := strconv.Atoi(parts[0])
		dels, _ := strconv.Atoi(parts[1])
		path := parts[2]
		files = append(files, FileChange{
			Path:      path,
			Additions: adds,
			Deletions: dels,
		})
	}

	// Also include untracked files (new files)
	cmd = exec.Command("git", "ls-files", "--others", "--exclude-standard")
	cmd.Dir = cwd
	out, err = cmd.Output()
	if err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if line == "" {
				continue
			}
			files = append(files, FileChange{
				Path:      line,
				Additions: 0,
				Deletions: 0,
			})
		}
	}

	return files
}
