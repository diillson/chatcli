/*
 * ChatCLI - Adapter binding the @tools meta-tool to the plugin registry.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Implements plugins.ToolCatalogAdapter over the live plugin manager:
 * Describe renders one tool's full prompt block through the SAME renderer the
 * agent system prompt uses (an on-demand definition is byte-identical to an
 * inline one), and List renders the one-line index of everything available.
 * Wired via plugins.SetToolCatalogAdapter at startup.
 */
package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/diillson/chatcli/cli/plugins"
)

// toolCatalogPluginAdapter is the concrete plugins.ToolCatalogAdapter.
type toolCatalogPluginAdapter struct {
	cli *ChatCLI
}

// Describe implements plugins.ToolCatalogAdapter.
func (a *toolCatalogPluginAdapter) Describe(name string) (string, error) {
	if a.cli.pluginManager == nil {
		return "", fmt.Errorf("plugin manager unavailable in this session")
	}
	want := strings.ToLower(strings.TrimSpace(name))
	if !strings.HasPrefix(want, "@") {
		want = "@" + want
	}
	ps := a.cli.pluginManager.GetPlugins()
	names := make([]string, 0, len(ps))
	for _, p := range ps {
		if strings.EqualFold(p.Name(), want) {
			return renderToolBlock(p, false), nil
		}
		names = append(names, p.Name())
	}
	sort.Strings(names)
	return "", fmt.Errorf("unknown tool %q — available: %s", name, strings.Join(names, ", "))
}

// List implements plugins.ToolCatalogAdapter.
func (a *toolCatalogPluginAdapter) List() (string, error) {
	if a.cli.pluginManager == nil {
		return "", fmt.Errorf("plugin manager unavailable in this session")
	}
	ps := a.cli.pluginManager.GetPlugins()
	sort.Slice(ps, func(i, j int) bool { return ps[i].Name() < ps[j].Name() })
	var b strings.Builder
	fmt.Fprintf(&b, "%d tool(s) available (describe any of them for full usage):\n", len(ps))
	for _, p := range ps {
		b.WriteString(renderToolIndexLine(p))
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// compile-time assertion that the adapter satisfies the plugin interface.
var _ plugins.ToolCatalogAdapter = (*toolCatalogPluginAdapter)(nil)
