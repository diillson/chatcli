package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Rule represents a single rule file with optional path-matching.
type Rule struct {
	Name    string   // filename without extension
	Content string   // markdown body (after frontmatter)
	Paths   []string // glob patterns; empty = applies globally
	ModTime time.Time
}

// RulesLoader scans and loads path-specific rules from .chatcli/rules/ directories.
// Rules are loaded lazily and cached with mtime invalidation.
type RulesLoader struct {
	workspaceDir string
	globalDir    string
	cache        map[string]*Rule // keyed by file path
	mu           sync.RWMutex
	logger       *zap.Logger
}

// NewRulesLoader creates a new rules loader.
func NewRulesLoader(workspaceDir, globalDir string, logger *zap.Logger) *RulesLoader {
	return &RulesLoader{
		workspaceDir: workspaceDir,
		globalDir:    globalDir,
		cache:        make(map[string]*Rule),
		logger:       logger,
	}
}

// LoadMatchingRules returns all rules that match the given file paths.
// If contextPaths is empty, returns only global rules (no paths: filter).
// Rules from workspace override global rules with the same name.
func (rl *RulesLoader) LoadMatchingRules(contextPaths []string) string {
	allRules := rl.loadAllRules()
	if len(allRules) == 0 {
		return ""
	}

	var matched []*Rule
	for _, rule := range allRules {
		if len(rule.Paths) == 0 {
			// Global rule — always include
			matched = append(matched, rule)
			continue
		}

		// Path-specific rule — check if any context path matches any rule glob
		for _, ruleGlob := range rule.Paths {
			if matchesAnyPath(ruleGlob, contextPaths) {
				matched = append(matched, rule)
				break
			}
		}
	}

	if len(matched) == 0 {
		return ""
	}

	var parts []string
	for _, rule := range matched {
		header := fmt.Sprintf("### Rule: %s", rule.Name)
		if len(rule.Paths) > 0 {
			header += fmt.Sprintf(" (paths: %s)", strings.Join(rule.Paths, ", "))
		}
		parts = append(parts, header+"\n\n"+rule.Content)
	}

	return "## Path-Specific Rules\n\n" + strings.Join(parts, "\n\n---\n\n")
}

// loadAllRules scans both global and workspace rules directories.
// Workspace rules override global rules with the same filename.
func (rl *RulesLoader) loadAllRules() []*Rule {
	seen := make(map[string]*Rule) // keyed by rule name (dedup)

	// Load global rules first (lower priority)
	globalRulesDir := filepath.Join(rl.globalDir, "rules")
	rl.scanRulesDir(globalRulesDir, seen)

	// Load workspace rules (higher priority — overwrites global)
	workspaceRulesDir := filepath.Join(rl.workspaceDir, ".chatcli", "rules")
	rl.scanRulesDir(workspaceRulesDir, seen)

	// Also check workspace root (for standalone .chatcli/rules/ without leading dot)
	altRulesDir := filepath.Join(rl.workspaceDir, "rules")
	if _, err := os.Stat(filepath.Join(rl.workspaceDir, ".chatcli")); err != nil {
		// No .chatcli dir, try bare rules/ in workspace
		rl.scanRulesDir(altRulesDir, seen)
	}

	rules := make([]*Rule, 0, len(seen))
	for _, rule := range seen {
		rules = append(rules, rule)
	}
	return rules
}

// scanRulesDir reads all .md files in a directory and loads them as rules.
func (rl *RulesLoader) scanRulesDir(dir string, seen map[string]*Rule) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return // Directory doesn't exist or can't be read
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		filePath := filepath.Join(dir, entry.Name())
		rule := rl.loadRuleFile(filePath)
		if rule != nil {
			seen[rule.Name] = rule // Overwrites global with workspace
		}
	}
}

// loadRuleFile loads a single rule file with caching.
func (rl *RulesLoader) loadRuleFile(filePath string) *Rule {
	info, err := os.Stat(filePath)
	if err != nil {
		return nil
	}

	// Check cache
	rl.mu.RLock()
	cached, found := rl.cache[filePath]
	rl.mu.RUnlock()

	if found && !info.ModTime().After(cached.ModTime) {
		return cached
	}

	// Read and parse
	data, err := os.ReadFile(filePath)
	if err != nil {
		rl.logger.Debug("failed to read rule file", zap.String("path", filePath), zap.Error(err))
		return nil
	}

	content := string(data)
	rule := &Rule{
		Name:    strings.TrimSuffix(filepath.Base(filePath), ".md"),
		ModTime: info.ModTime(),
	}

	// Parse simple frontmatter for paths
	if strings.HasPrefix(content, "---") {
		parts := strings.SplitN(content[3:], "---", 2)
		if len(parts) == 2 {
			frontmatter := parts[0]
			rule.Content = strings.TrimSpace(parts[1])

			// Extract paths from frontmatter
			inPathsList := false
			for _, line := range strings.Split(frontmatter, "\n") {
				trimmed := strings.TrimSpace(line)

				if strings.HasPrefix(trimmed, "paths:") {
					inPathsList = true
					pathsStr := strings.TrimPrefix(trimmed, "paths:")
					pathsStr = strings.TrimSpace(pathsStr)

					// Handle inline array: paths: ["src/**", "lib/**"]
					if strings.HasPrefix(pathsStr, "[") {
						pathsStr = strings.Trim(pathsStr, "[]")
						for _, p := range strings.Split(pathsStr, ",") {
							p = strings.TrimSpace(p)
							p = strings.Trim(p, `"'`)
							if p != "" {
								rule.Paths = append(rule.Paths, p)
							}
						}
						inPathsList = false // inline array complete
					}
					continue
				}

				// YAML list items under paths:
				if inPathsList && strings.HasPrefix(trimmed, "- ") {
					p := strings.TrimPrefix(trimmed, "- ")
					p = strings.Trim(p, `"'`)
					p = strings.TrimSpace(p)
					if p != "" {
						rule.Paths = append(rule.Paths, p)
					}
					continue
				}

				// Any non-list-item line ends the paths list
				if inPathsList && trimmed != "" && !strings.HasPrefix(trimmed, "#") {
					inPathsList = false
				}
			}
		} else {
			rule.Content = content
		}
	} else {
		rule.Content = content
	}

	if strings.TrimSpace(rule.Content) == "" {
		return nil
	}

	// Cache
	rl.mu.Lock()
	rl.cache[filePath] = rule
	rl.mu.Unlock()

	rl.logger.Debug("loaded rule",
		zap.String("name", rule.Name),
		zap.Strings("paths", rule.Paths),
		zap.String("file", filePath))

	return rule
}

// matchesAnyPath checks if a glob pattern matches any of the given paths.
// Supports: *.go, src/**, **/*.go, exact/path.go
func matchesAnyPath(pattern string, paths []string) bool {
	for _, p := range paths {
		if matchGlob(pattern, p) {
			return true
		}
	}
	return false
}

// matchGlob matches a single pattern against a single path.
// Handles ** (recursive wildcard) which filepath.Match doesn't support.
func matchGlob(pattern, path string) bool {
	// 1. Exact match via filepath.Match (handles *, ?)
	if matched, err := filepath.Match(pattern, path); err == nil && matched {
		return true
	}

	// 2. Match filename alone (e.g., pattern "*.go" matches "src/main.go")
	base := filepath.Base(path)
	if !strings.Contains(pattern, "/") && !strings.Contains(pattern, "**") {
		if matched, err := filepath.Match(pattern, base); err == nil && matched {
			return true
		}
	}

	// 3. Handle ** patterns (recursive directory matching)
	if strings.Contains(pattern, "**") {
		parts := strings.SplitN(pattern, "**", 2)
		prefix := parts[0]
		suffix := ""
		if len(parts) > 1 {
			suffix = strings.TrimPrefix(parts[1], "/")
		}

		// Check prefix match
		if prefix != "" && !strings.HasPrefix(path, prefix) {
			return false
		}

		// If no suffix, prefix match is enough (e.g., "src/**" matches anything under src/)
		if suffix == "" {
			return strings.HasPrefix(path, prefix)
		}

		// Check suffix match (e.g., "**/*.go" — check if file ends with .go)
		remainder := strings.TrimPrefix(path, prefix)
		// Try matching the suffix against each possible tail of the path
		pathParts := strings.Split(remainder, "/")
		for i := range pathParts {
			tail := strings.Join(pathParts[i:], "/")
			if matched, err := filepath.Match(suffix, tail); err == nil && matched {
				return true
			}
			// Also try just the last component
			if matched, err := filepath.Match(suffix, pathParts[len(pathParts)-1]); err == nil && matched {
				return true
			}
		}
	}

	return false
}
