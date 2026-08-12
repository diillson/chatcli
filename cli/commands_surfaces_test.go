/*
 * ChatCLI - Slash command surface coverage (config, palette, completer,
 * ACP listing, @commands adapter).
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 */
package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/palette"
)

func TestConfigCommandsSection_ShowAndRoute(t *testing.T) {
	cli, project := newCommandsTestCLI(t)
	writeCommandFile(t, project, "deploy.md", "---\ndescription: Deploy service\n---\nDeploy $1")
	writeCommandFile(t, project, "session.md", "hijack") // refused → diagnostics branch

	// Panorama and mutation paths must run clean end to end (print-only).
	cli.showConfigCommands()
	cli.routeConfigCommands([]string{"reload"})
	cli.routeConfigCommands([]string{"bogus"})

	// Disabled branch.
	t.Setenv("CHATCLI_COMMANDS", "off")
	cli.showConfigCommands()
}

func TestCommandsPluginAdapter_ListAndGet(t *testing.T) {
	cli, project := newCommandsTestCLI(t)
	writeCommandFile(t, project, "triage.md",
		"---\ndescription: Triage an issue\nargument-hint: <issue>\n---\nTriage $1 now.")
	a := &commandsPluginAdapter{cli: cli}

	list := a.List()
	if !strings.Contains(list, "triage") || !strings.Contains(list, "<issue>") {
		t.Errorf("adapter list must carry names and hints: %q", list)
	}
	out, ok := a.Get(context.Background(), "triage", "42")
	if !ok || !strings.Contains(out, "Triage 42 now.") {
		t.Errorf("adapter get failed: %q ok=%v", out, ok)
	}
	if _, ok := a.Get(context.Background(), "nope", ""); ok {
		t.Error("unknown name must return ok=false")
	}

	// Empty catalog message.
	empty, _ := newCommandsTestCLI(t)
	if msg := (&commandsPluginAdapter{cli: empty}).List(); !strings.Contains(msg, "No slash commands") {
		t.Errorf("empty catalog must explain how to create commands: %q", msg)
	}
}

func TestGetSlashCommandSuggestions(t *testing.T) {
	cli, project := newCommandsTestCLI(t)
	writeCommandFile(t, project, "review-pr.md",
		"---\ndescription: Review a PR\nargument-hint: <pr>\n---\nbody")
	writeCommandFile(t, project, "bare.md", "body only")

	sugg := cli.getSlashCommandSuggestions()
	if len(sugg) != 2 {
		t.Fatalf("expected 2 suggestions, got %v", sugg)
	}
	byText := map[string]string{}
	for _, s := range sugg {
		byText[s.Text] = s.Description
	}
	if desc := byText["/review-pr"]; !strings.Contains(desc, "Review a PR") || !strings.Contains(desc, "<pr>") {
		t.Errorf("suggestion must merge description and hint: %q", desc)
	}
	if byText["/bare"] == "" {
		t.Error("description-less command must fall back to the i18n label")
	}
}

func TestPaletteProvider_CommandsAndSkillsAppear(t *testing.T) {
	cli, project := newCommandsTestCLI(t)
	writeCommandFile(t, project, "release.md", "---\ndescription: Cut a release\n---\nbody")
	cli.registerCommandPaletteProvider()

	var found *palette.RootCommand
	for _, rc := range palette.AllRootCommands() {
		if rc.Name == "/release" {
			c := rc
			found = &c
			break
		}
	}
	if found == nil {
		t.Fatal("dynamic command must appear in the palette root listing")
	}
	if found.Category != palette.CatCommands || found.Summary() != "Cut a release" {
		t.Errorf("palette entry mismatch: %+v", found)
	}
	// Static entries always win a collision and stay intact.
	if sum, ok := palette.RootSummary("/help"); !ok || sum == "" {
		t.Error("static registry must remain authoritative")
	}
}

func TestListACPCommands_IncludesTemplates(t *testing.T) {
	cli, project := newCommandsTestCLI(t)
	writeCommandFile(t, project, "frontend", "") // namespace dir marker (ignored)
	writeCommandFile(t, project, "audit.md",
		"---\ndescription: Audit deps\nargument-hint: <scope>\n---\nbody")

	found := false
	for _, c := range cli.ListACPCommands() {
		if c.Name == "audit" {
			found = true
			if c.Description != "Audit deps" || c.InputHint != "<scope>" {
				t.Errorf("ACP entry mismatch: %+v", c)
			}
		}
	}
	if !found {
		t.Fatal("templates must be advertised over ACP")
	}
}

func TestSplitSlashInvocation(t *testing.T) {
	if tok, args, ok := splitSlashInvocation("/review-pr 1 2"); !ok || tok != "review-pr" || args != "1 2" {
		t.Errorf("split mismatch: %q %q %v", tok, args, ok)
	}
	for _, bad := range []string{"", "/", "no-slash", "/ "} {
		if _, _, ok := splitSlashInvocation(bad); ok {
			t.Errorf("%q must not parse as an invocation", bad)
		}
	}
}
