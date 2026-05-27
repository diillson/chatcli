/*
 * ChatCLI - memory_adapter.go
 *
 * Implements plugins.MemoryAdapter so the @memory builtin tool can route
 * ReAct calls into the live memory store. Supplied to
 * plugins.SetMemoryAdapter when the memory store is initialized.
 *
 * Writes go through the deterministic store methods (RememberFact /
 * UpdateProfile / ForgetFacts) — no LLM, no throttling — and invalidate the
 * context builder cache so the next prompt reflects the change.
 */
package cli

import (
	"fmt"
	"strings"

	"github.com/diillson/chatcli/i18n"
)

// memoryPluginAdapter is the concrete plugins.MemoryAdapter.
type memoryPluginAdapter struct {
	cli *ChatCLI
}

// invalidate refreshes the context builder so freshly written memory is
// visible on the next turn. No-op when the builder is absent.
func (a *memoryPluginAdapter) invalidate() {
	if a.cli.contextBuilder != nil {
		a.cli.contextBuilder.InvalidateCache()
	}
}

func (a *memoryPluginAdapter) Remember(content, category string) (string, error) {
	if a.cli.memoryStore == nil {
		return "", fmt.Errorf("memory not enabled")
	}
	added := a.cli.memoryStore.RememberFact(content, category)
	a.invalidate()
	if !added {
		return i18n.T("mem.tool.remember.dup"), nil
	}
	return i18n.T("mem.tool.remember.ok", content), nil
}

func (a *memoryPluginAdapter) UpdateProfile(updates map[string]string) (string, error) {
	if a.cli.memoryStore == nil {
		return "", fmt.Errorf("memory not enabled")
	}
	changed := a.cli.memoryStore.UpdateProfile(updates)
	a.invalidate()
	if !changed {
		return i18n.T("mem.tool.profile.nochange"), nil
	}
	keys := make([]string, 0, len(updates))
	for k := range updates {
		keys = append(keys, k)
	}
	return i18n.T("mem.tool.profile.ok", strings.Join(keys, ", ")), nil
}

func (a *memoryPluginAdapter) Forget(match string) (string, error) {
	if a.cli.memoryStore == nil {
		return "", fmt.Errorf("memory not enabled")
	}
	n := a.cli.memoryStore.ForgetFacts(match)
	a.invalidate()
	return i18n.T("mem.tool.forget.result", n), nil
}

func (a *memoryPluginAdapter) Recall(query string) (string, error) {
	if a.cli.memoryStore == nil {
		return "", fmt.Errorf("memory not enabled")
	}
	var ctx string
	if q := strings.TrimSpace(query); q != "" {
		ctx = a.cli.memoryStore.GetRelevantContext(strings.Fields(q))
	} else {
		ctx = a.cli.memoryStore.GetMemoryContext()
	}
	if strings.TrimSpace(ctx) == "" {
		return i18n.T("mem.tool.recall.empty"), nil
	}
	return ctx, nil
}
