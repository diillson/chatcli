package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/cli/workspace/threatscan"
	"go.uber.org/zap"
)

// BootstrapFiles are loaded in this order (all optional).
var BootstrapFiles = []string{
	"AGENTS.md",
	"SOUL.md",
	"USER.md",
	"IDENTITY.md",
	"RULES.md",
}

// BootstrapLoader loads and caches workspace bootstrap files.
type BootstrapLoader struct {
	workspaceDir string
	globalDir    string // ~/.chatcli/
	cache        map[string]cachedFile
	mu           sync.RWMutex
	logger       *zap.Logger
}

type cachedFile struct {
	content string
	modTime time.Time
}

// NewBootstrapLoader creates a new loader.
func NewBootstrapLoader(workspaceDir, globalDir string, logger *zap.Logger) *BootstrapLoader {
	return &BootstrapLoader{
		workspaceDir: workspaceDir,
		globalDir:    globalDir,
		cache:        make(map[string]cachedFile),
		logger:       logger,
	}
}

// LoadBootstrapContent loads all bootstrap files with mtime-based cache invalidation.
// Priority: workspace > global (workspace files override global).
func (bl *BootstrapLoader) LoadBootstrapContent() string {
	var parts []string

	for _, filename := range BootstrapFiles {
		content, ok := bl.LoadFile(filename)
		if ok && strings.TrimSpace(content) != "" {
			parts = append(parts, fmt.Sprintf("## %s\n\n%s", filename, content))
		}
	}

	if len(parts) == 0 {
		return ""
	}
	content := strings.Join(parts, "\n\n---\n\n")

	// Neutralize blatant prompt-injection directives that may have been
	// planted in a bootstrap file (e.g. a cloned repo's AGENTS.md) before
	// the text reaches the model. Conservative, line-level — legitimate
	// shell snippets in context files are left untouched (ScopeContext).
	if threatscan.Enabled() {
		if sanitized, blocked := threatscan.Sanitize(content, threatscan.ScopeContext); blocked > 0 {
			bl.logger.Warn("threatscan: neutralized lines in bootstrap context",
				zap.Int("blocked", blocked))
			content = sanitized
		}
	}
	return content
}

// LoadFile loads a single bootstrap file (workspace first, then global).
func (bl *BootstrapLoader) LoadFile(filename string) (string, bool) {
	// Try workspace first
	workspacePath := filepath.Join(bl.workspaceDir, filename)
	if content, ok := bl.loadWithCache(workspacePath); ok {
		return content, true
	}

	// Fall back to global
	globalPath := filepath.Join(bl.globalDir, filename)
	if content, ok := bl.loadWithCache(globalPath); ok {
		return content, true
	}

	return "", false
}

// IsStale checks if any cached file has been modified on disk.
func (bl *BootstrapLoader) IsStale() bool {
	bl.mu.RLock()
	defer bl.mu.RUnlock()

	for path, cached := range bl.cache {
		info, err := os.Stat(path)
		if err != nil {
			// File was deleted; cache is stale
			if cached.content != "" {
				return true
			}
			continue
		}
		if info.ModTime().After(cached.modTime) {
			return true
		}
	}
	return false
}

// InvalidateCache forces reload on next call.
func (bl *BootstrapLoader) InvalidateCache() {
	bl.mu.Lock()
	defer bl.mu.Unlock()
	bl.cache = make(map[string]cachedFile)
}

func (bl *BootstrapLoader) loadWithCache(path string) (string, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return "", false
	}

	bl.mu.RLock()
	cached, found := bl.cache[path]
	bl.mu.RUnlock()

	if found && !info.ModTime().After(cached.modTime) {
		return cached.content, true
	}

	data, err := os.ReadFile(path) //#nosec G304 -- path supplied by user/agent through validated tool surface (boundary check upstream)
	if err != nil {
		bl.logger.Debug("failed to read bootstrap file", zap.String("path", path), zap.Error(err))
		return "", false
	}

	content := string(data)
	bl.mu.Lock()
	bl.cache[path] = cachedFile{
		content: content,
		modTime: info.ModTime(),
	}
	bl.mu.Unlock()

	return content, true
}
