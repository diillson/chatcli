/*
 * ChatCLI - tests for the @tools catalog adapter and /config agent catalog.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/plugins"
	"go.uber.org/zap"
)

func newCatalogCLI(t *testing.T) *ChatCLI {
	t.Helper()
	mgr, err := plugins.NewManager(zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	mgr.RegisterBuiltinPlugin(plugins.NewBuiltinDiagramPlugin())
	mgr.RegisterBuiltinPlugin(plugins.NewBuiltinToolsPlugin())
	return &ChatCLI{pluginManager: mgr, logger: zap.NewNop()}
}

// TestToolCatalogAdapterDescribe pins the meta-tool's core promise: the
// on-demand definition is the SAME full block the inline prompt would carry.
func TestToolCatalogAdapterDescribe(t *testing.T) {
	cli := newCatalogCLI(t)
	a := &toolCatalogPluginAdapter{cli: cli}

	out, err := a.Describe("@diagram")
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if !strings.Contains(out, "- Ferramenta: @diagram") || !strings.Contains(out, "Subcomandos Disponíveis:") {
		t.Fatalf("Describe must return the full prompt block, got: %.120s", out)
	}

	// Name without the @ prefix resolves too (models drop it routinely).
	if _, err := a.Describe("diagram"); err != nil {
		t.Fatalf("prefixless describe: %v", err)
	}

	// Unknown tool errors and lists what exists.
	if _, err := a.Describe("@nope"); err == nil || !strings.Contains(err.Error(), "@diagram") {
		t.Fatalf("unknown tool must list available names, got %v", err)
	}
}

// TestToolCatalogAdapterList pins the index rendering.
func TestToolCatalogAdapterList(t *testing.T) {
	cli := newCatalogCLI(t)
	a := &toolCatalogPluginAdapter{cli: cli}

	out, err := a.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !strings.Contains(out, "- @diagram: ") || !strings.Contains(out, "- @tools: ") {
		t.Fatalf("List missing index lines: %q", out)
	}
	if strings.Contains(out, "Subcomandos") {
		t.Fatal("List must stay one line per tool — no schemas")
	}
}

// TestConfigAgentCatalogSwitch pins the runtime flip: the mutator mirrors the
// env var, and toolCatalogDeferred reflects it immediately.
func TestConfigAgentCatalogSwitch(t *testing.T) {
	prev, had := os.LookupEnv(toolCatalogEnvVar)
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(toolCatalogEnvVar, prev)
		} else {
			_ = os.Unsetenv(toolCatalogEnvVar)
		}
	})

	cli := newCatalogCLI(t)
	cli.configAgentCatalog([]string{"full"})
	if toolCatalogDeferred() {
		t.Fatal("catalog full did not flip the runtime mode")
	}
	cli.configAgentCatalog([]string{"deferred"})
	if !toolCatalogDeferred() {
		t.Fatal("catalog deferred did not flip back")
	}
	// Invalid value: mode unchanged.
	cli.configAgentCatalog([]string{"banana"})
	if !toolCatalogDeferred() {
		t.Fatal("invalid value must not change the mode")
	}
	// No-arg show path must not panic.
	cli.configAgentCatalog(nil)
}
