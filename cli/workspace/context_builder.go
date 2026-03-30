package workspace

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// ContextBuilder assembles the full system prompt from workspace sources.
type ContextBuilder struct {
	bootstrap    *BootstrapLoader
	memory       *MemoryStore
	rules        *RulesLoader
	workspaceDir string // current working directory / project root
	cache        *promptCache
	mu           sync.RWMutex
}

type promptCache struct {
	content string
	builtAt time.Time
	stale   bool
}

// NewContextBuilder creates a new context builder.
// workspaceDir is the detected project root (or CWD) where the session started.
func NewContextBuilder(bootstrap *BootstrapLoader, memory *MemoryStore, workspaceDir string) *ContextBuilder {
	cb := &ContextBuilder{
		bootstrap:    bootstrap,
		memory:       memory,
		workspaceDir: workspaceDir,
	}

	// Initialize path-specific rules loader
	if bootstrap != nil {
		globalDir := ""
		if bootstrap.globalDir != "" {
			globalDir = bootstrap.globalDir
		}
		cb.rules = NewRulesLoader(workspaceDir, globalDir, bootstrap.logger)
	}

	return cb
}

// WorkspaceDir returns the current workspace directory.
func (cb *ContextBuilder) WorkspaceDir() string {
	return cb.workspaceDir
}

// BuildSystemPromptPrefix returns workspace context to prepend to the system prompt.
// Includes: bootstrap files + memory context.
// Does NOT include mode-specific instructions (coder/agent prompts).
func (cb *ContextBuilder) BuildSystemPromptPrefix() string {
	return cb.BuildSystemPromptPrefixWithHints(nil)
}

// BuildSystemPromptPrefixWithHints returns workspace context with memory
// retrieval tailored to the given conversation hints (keywords).
func (cb *ContextBuilder) BuildSystemPromptPrefixWithHints(hints []string) string {
	// When hints are provided, skip cache so retrieval reflects current context
	if len(hints) == 0 {
		cb.mu.RLock()
		if cb.cache != nil && !cb.cache.stale && !cb.bootstrap.IsStale() {
			content := cb.cache.content
			cb.mu.RUnlock()
			return content
		}
		cb.mu.RUnlock()
	}

	var parts []string

	// Bootstrap files (SOUL.md, USER.md, etc.)
	bootstrapContent := cb.bootstrap.LoadBootstrapContent()
	if bootstrapContent != "" {
		parts = append(parts, bootstrapContent)
	}

	// Memory context — smart retrieval with hints
	if cb.memory != nil {
		var memoryContent string
		if len(hints) > 0 {
			memoryContent = cb.memory.GetRelevantContext(hints)
		} else {
			memoryContent = cb.memory.GetMemoryContext()
		}
		if memoryContent != "" {
			parts = append(parts, "# Memory\n\n"+memoryContent)
		}
	}

	// Path-specific rules — loaded lazily based on file-like hints
	if cb.rules != nil && len(hints) > 0 {
		// Filter hints to only those that look like file paths
		// (contain dots or slashes), which avoids matching generic keywords
		var pathHints []string
		for _, h := range hints {
			if strings.Contains(h, ".") || strings.Contains(h, "/") {
				pathHints = append(pathHints, h)
			}
		}
		if len(pathHints) > 0 {
			rulesContent := cb.rules.LoadMatchingRules(pathHints)
			if rulesContent != "" {
				parts = append(parts, rulesContent)
			}
		}
	}

	content := ""
	if len(parts) > 0 {
		content = strings.Join(parts, "\n\n---\n\n")
	}

	// Only cache when no hints (generic context)
	if len(hints) == 0 {
		cb.mu.Lock()
		cb.cache = &promptCache{
			content: content,
			builtAt: time.Now(),
			stale:   false,
		}
		cb.mu.Unlock()
	}

	return content
}

// BuildDynamicContext returns time-sensitive and session-aware context.
// Includes current time, working directory, and disambiguation instructions
// so the model never confuses paths from long-term memory with the current session.
func (cb *ContextBuilder) BuildDynamicContext() string {
	now := time.Now()
	var parts []string
	parts = append(parts, fmt.Sprintf("Current date and time: %s", now.Format("2006-01-02 15:04:05 MST")))

	if cb.workspaceDir != "" {
		parts = append(parts, fmt.Sprintf("Current working directory: %s", cb.workspaceDir))
		parts = append(parts,
			"IMPORTANT: When the user refers to \"here\", \"this project\", \"current directory\", "+
				"or uses relative paths, ALWAYS resolve them against the current working directory above — "+
				"NOT against paths from long-term memory or previous sessions. "+
				"Memory may contain paths from other projects; treat those as historical context only.")
	}

	return strings.Join(parts, "\n")
}

// InvalidateCache forces rebuild on next call.
func (cb *ContextBuilder) InvalidateCache() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.cache != nil {
		cb.cache.stale = true
	}
}
