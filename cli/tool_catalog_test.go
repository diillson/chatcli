/*
 * ChatCLI - tests for the deferred tool catalog.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/plugins"
)

// catalogPlugins builds the full builtin set the way cli.go registers it.
func catalogPlugins() []plugins.Plugin {
	return []plugins.Plugin{
		plugins.NewBuiltinAskPlugin(), plugins.NewBuiltinCoderPlugin(), plugins.NewBuiltinCompressPlugin(),
		plugins.NewBuiltinContextPlugin(), plugins.NewBuiltinDiagramPlugin(), plugins.NewBuiltinDocsFlattenPlugin(),
		plugins.NewBuiltinGraphViewPlugin(), plugins.NewBuiltinImagePlugin(), plugins.NewBuiltinKnowledgePlugin(),
		plugins.NewBuiltinLSPPlugin(), plugins.NewBuiltinMemoryPlugin(), plugins.NewBuiltinMoaPlugin(),
		plugins.NewBuiltinOsvPlugin(), plugins.NewBuiltinParkPlugin(), plugins.NewBuiltinReadPlugin(),
		plugins.NewBuiltinRecallPlugin(), plugins.NewBuiltinRegistryTagsPlugin(), plugins.NewBuiltinSchedulerPlugin(),
		plugins.NewBuiltinSearchPlugin(), plugins.NewBuiltinSendPlugin(), plugins.NewBuiltinSessionPlugin(),
		plugins.NewBuiltinSkillPlugin(), plugins.NewBuiltinSpeakPlugin(), plugins.NewBuiltinTodoPlugin(),
		plugins.NewBuiltinToolsPlugin(), plugins.NewBuiltinTreePlugin(), plugins.NewBuiltinWebFetchPlugin(),
		plugins.NewBuiltinWebSearchPlugin(), plugins.NewBuiltinWikipediaPlugin(),
	}
}

// renderCatalog assembles the tool section the way getToolContextString does,
// for a given deferral state — the measurable core of the prompt builder.
func renderCatalog(ps []plugins.Plugin, deferred bool) string {
	var blocks, index []string
	for _, p := range ps {
		if deferred && p.Path() == "" && !isCoreTool(p.Name()) {
			index = append(index, renderToolIndexLine(p))
			continue
		}
		blocks = append(blocks, renderToolBlock(p, false))
	}
	out := strings.Join(blocks, "\n")
	if len(index) > 0 {
		out += "\n\n" + deferredCatalogInstruction + strings.Join(index, "")
	}
	return out
}

// TestDeferredCatalogKeepsCoreAndIndexesRest pins the tiering contract: core
// tools keep their full definition (subcommand lists present), deferred tools
// appear exactly once — as an index line, with no schema details inline.
func TestDeferredCatalogKeepsCoreAndIndexesRest(t *testing.T) {
	out := renderCatalog(catalogPlugins(), true)

	// Core: full definition present.
	if !strings.Contains(out, "- Ferramenta: @coder") {
		t.Fatal("@coder (core) must keep its full definition")
	}
	if !strings.Contains(out, "- Ferramenta: @tools") {
		t.Fatal("@tools (the catalog key) must keep its full definition")
	}

	// Deferred: index line yes, full block no.
	if strings.Contains(out, "- Ferramenta: @diagram") {
		t.Fatal("@diagram must be deferred to the index, not fully rendered")
	}
	if !strings.Contains(out, "- @diagram: ") {
		t.Fatal("@diagram missing from the index")
	}
	if !strings.Contains(out, "@tools") || !strings.Contains(out, "Tool index:") {
		t.Fatal("index section must carry the @tools expansion instruction")
	}
}

// TestFullCatalogKeepsLegacyShape pins the opt-out: with deferral off, every
// builtin renders its full block and no index section exists.
func TestFullCatalogKeepsLegacyShape(t *testing.T) {
	out := renderCatalog(catalogPlugins(), false)
	if !strings.Contains(out, "- Ferramenta: @diagram") {
		t.Fatal("full mode must render every tool's definition")
	}
	if strings.Contains(out, "Tool index:") {
		t.Fatal("full mode must not emit an index section")
	}
}

// TestDeferredCatalogSavesTokens pins the reason this exists with numbers:
// the deferred rendering must be at most 40% of the full one (measured at
// ~11k → ~2.5k tokens when built).
func TestDeferredCatalogSavesTokens(t *testing.T) {
	ps := catalogPlugins()
	full := len(renderCatalog(ps, false))
	deferred := len(renderCatalog(ps, true))
	t.Logf("catalog bytes: full=%d (~%d tok) deferred=%d (~%d tok) — saving %d%%",
		full, full/4, deferred, deferred/4, 100-(deferred*100/full))
	if deferred*100/full > 40 {
		t.Fatalf("deferred catalog is %d bytes (%d%% of full %d) — deferral regressed",
			deferred, deferred*100/full, full)
	}
}

// TestToolCatalogEnvSwitch pins the env contract: default deferred, "full"
// opts out.
func TestToolCatalogEnvSwitch(t *testing.T) {
	t.Setenv(toolCatalogEnvVar, "")
	if !toolCatalogDeferred() {
		t.Fatal("default must be deferred")
	}
	t.Setenv(toolCatalogEnvVar, "full")
	if toolCatalogDeferred() {
		t.Fatal("full must disable deferral")
	}
	t.Setenv(toolCatalogEnvVar, "deferred")
	if !toolCatalogDeferred() {
		t.Fatal("explicit deferred must defer")
	}
}

// TestFirstSentenceBounds pins the index-line trimmer.
func TestFirstSentenceBounds(t *testing.T) {
	if got := firstSentence("Does a thing. And more detail here."); got != "Does a thing." {
		t.Fatalf("first sentence = %q", got)
	}
	long := strings.Repeat("ção sem pontuação ", 30)
	got := firstSentence(long)
	if len(got) > 170 {
		t.Fatalf("unterminated description not bounded: %d bytes", len(got))
	}
}

// TestNewToolsRenderSubcommands guards the Schema contract: @tools describe
// renders a plugin's subcommands only when its Schema() uses the "subcommands"
// key. The first cut of @browser/@forge/@view used "commands", so describe
// showed an empty subcommand list and the model could not learn the format.
func TestNewToolsRenderSubcommands(t *testing.T) {
	cases := []struct {
		p    plugins.Plugin
		want string // a subcommand that must appear in the rendered block
	}{
		{plugins.NewBuiltinBrowserPlugin(), "open"},
		{plugins.NewBuiltinForgePlugin(), "pr-view"},
		{plugins.NewBuiltinViewPlugin(), "view"},
	}
	for _, tc := range cases {
		block := renderToolBlock(tc.p, false)
		if !strings.Contains(block, "Subcomandos Disponíveis") {
			t.Fatalf("%s: describe block has no subcommand section:\n%s", tc.p.Name(), block)
		}
		if !strings.Contains(block, tc.want) {
			t.Fatalf("%s: describe block missing subcommand %q — Schema() likely uses \"commands\" not \"subcommands\":\n%s", tc.p.Name(), tc.want, block)
		}
	}
}
