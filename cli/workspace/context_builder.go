package workspace

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// ContextBuilder assembles the full system prompt from workspace sources.
type ContextBuilder struct {
	bootstrap *BootstrapLoader
	memory    *MemoryStore
	cache     *promptCache
	mu        sync.RWMutex
}

type promptCache struct {
	content string
	builtAt time.Time
	stale   bool
}

// NewContextBuilder creates a new context builder.
func NewContextBuilder(bootstrap *BootstrapLoader, memory *MemoryStore) *ContextBuilder {
	return &ContextBuilder{
		bootstrap: bootstrap,
		memory:    memory,
	}
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

// BuildDynamicContext returns time-sensitive context.
func (cb *ContextBuilder) BuildDynamicContext() string {
	now := time.Now()
	return fmt.Sprintf("Current date and time: %s", now.Format("2006-01-02 15:04:05 MST"))
}

// InvalidateCache forces rebuild on next call.
func (cb *ContextBuilder) InvalidateCache() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.cache != nil {
		cb.cache.stale = true
	}
}
