/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"bytes"
	"sort"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/palette"
	"go.uber.org/zap"
)

// routedButNotListed are names the router reserves that are deliberately
// absent from the command registry (and so from /help), each with a reason.
var routedButNotListed = map[string]string{
	"fast": "reserved for a future mode; it has no handler and must not be advertised",
}

// registryNames returns every root command name and alias, slash included.
func registryNames() map[string]bool {
	names := map[string]bool{}
	for _, rc := range palette.RootCommands() {
		names[rc.Name] = true
		for _, a := range palette.Aliases(rc.Name) {
			names[a] = true
		}
	}
	return names
}

// Every command the router dispatches must be listed (by name or alias),
// so /help, the palette and the docs can never silently miss one again.
func TestEveryRoutedCommandIsInTheRegistry(t *testing.T) {
	ch := NewCommandHandler(&ChatCLI{logger: zap.NewNop()})
	listed := registryNames()
	var missing []string
	for name := range ch.routes.reserved {
		if _, exempt := routedButNotListed[name]; exempt {
			continue
		}
		if !listed["/"+name] {
			missing = append(missing, "/"+name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("routed commands missing from the palette registry (cli/palette/registry.go rootCommands or rootAliases): %v", missing)
	}
}

// …and every listed command must actually route: help never advertises a
// command that does nothing.
func TestEveryRegistryCommandRoutes(t *testing.T) {
	ch := NewCommandHandler(&ChatCLI{logger: zap.NewNop()})
	for name := range registryNames() {
		if !ch.routes.reserved[strings.TrimPrefix(name, "/")] {
			t.Errorf("%s is listed but the router does not dispatch it", name)
		}
	}
	for name := range routedButNotListed {
		if registryNames()["/"+name] {
			t.Errorf("/%s is exempt from listing but is listed", name)
		}
	}
}

// The inline completer offers exactly the registry's commands (aliases may
// appear too).
func TestCompleterMatchesRegistry(t *testing.T) {
	c := &ChatCLI{logger: zap.NewNop()}
	listed := registryNames()
	offered := map[string]bool{}
	for _, s := range c.GetInternalCommands() {
		offered[s.Text] = true
		if !listed[s.Text] {
			t.Errorf("completer offers %s, which the registry does not list", s.Text)
		}
	}
	for _, rc := range palette.RootCommands() {
		if !offered[rc.Name] {
			t.Errorf("registry lists %s, which the completer does not offer", rc.Name)
		}
	}
}

func renderPlainHelp(t *testing.T) string {
	t.Helper()
	var b bytes.Buffer
	(&ChatCLI{logger: zap.NewNop()}).renderHelp(&b, helpStyle{})
	return b.String()
}

// /help is generated: every command, alias, subcommand and flag appears.
func TestHelpListsEverything(t *testing.T) {
	out := renderPlainHelp(t)
	for name := range registryNames() {
		if !strings.Contains(out, name) {
			t.Errorf("/help lacks %s", name)
		}
	}
	for _, sc := range Subcommands() {
		if !strings.Contains(out, "chatcli "+sc.Name) {
			t.Errorf("/help lacks subcommand %s", sc.Name)
		}
		for _, a := range sc.Aliases {
			if !strings.Contains(out, a) {
				t.Errorf("/help lacks subcommand alias %s", a)
			}
		}
	}
	for _, f := range OneShotFlags() {
		if !strings.Contains(out, f.Syntax) {
			t.Errorf("/help lacks flag %s", f.Syntax)
		}
	}
	for _, side := range sideCommandRoots {
		if !strings.Contains(out, side) {
			t.Errorf("/help lacks mid-run command %s", side)
		}
	}
	if strings.Contains(out, "\x1b[") {
		t.Error("the plain rendering (LLM catalog) must carry no ANSI escapes")
	}
}

// The model's catalog is the same generated listing.
func TestHelpTextIsTheGeneratedCatalog(t *testing.T) {
	got := (&ChatCLI{logger: zap.NewNop()}).helpText()
	if got != renderPlainHelp(t) {
		t.Fatal("helpText must be the plain rendering of /help")
	}
}

func TestCommandHelp(t *testing.T) {
	c := &ChatCLI{logger: zap.NewNop()}
	var b bytes.Buffer
	if !c.renderCommandHelp(&b, helpStyle{}, "session") {
		t.Fatal("/help session must resolve without the slash")
	}
	if !strings.Contains(b.String(), "/session") || !strings.Contains(b.String(), "save") {
		t.Errorf("/help session must list the completer's subcommands: %s", b.String())
	}
	b.Reset()
	if !c.renderCommandHelp(&b, helpStyle{}, "/status") || !strings.Contains(b.String(), "/config") {
		t.Errorf("an alias resolves to its command: %s", b.String())
	}
	if c.renderCommandHelp(&b, helpStyle{}, "/nope") {
		t.Error("unknown commands are reported as unknown")
	}
}

func TestSubcommandRegistry(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range Subcommands() {
		for _, n := range append([]string{s.Name}, s.Aliases...) {
			if seen[n] {
				t.Errorf("duplicate subcommand name %s", n)
			}
			seen[n] = true
			if got, ok := LookupSubcommand(n); !ok || got.Name != s.Name {
				t.Errorf("lookup %s", n)
			}
		}
		if s.Usage == "" || !strings.HasPrefix(s.Usage, "chatcli "+s.Name) {
			t.Errorf("%s: usage must start with its invocation: %q", s.Name, s.Usage)
		}
	}
	if _, ok := LookupSubcommand("nope"); ok {
		t.Error("unknown subcommand")
	}
	for _, args := range [][]string{{"-h"}, {"--help"}, {"help"}} {
		if !IsHelpArg(args) {
			t.Errorf("%v is a help request", args)
		}
	}
	if IsHelpArg(nil) || IsHelpArg([]string{"run"}) {
		t.Error("not a help request")
	}
	var b bytes.Buffer
	PrintUsage(&b)
	for _, s := range Subcommands() {
		if !strings.Contains(b.String(), s.Name) {
			t.Errorf("chatcli --help lacks %s", s.Name)
		}
	}
	b.Reset()
	sc, _ := LookupSubcommand("mcp-server")
	PrintSubcommandUsage(&b, sc)
	if !strings.Contains(b.String(), "chatcli mcp-server") || !strings.Contains(b.String(), "mcp-serve") {
		t.Errorf("generic usage: %s", b.String())
	}
}

func TestOneShotFlagsFoldAliases(t *testing.T) {
	var syntaxes []string
	for _, f := range OneShotFlags() {
		syntaxes = append(syntaxes, f.Syntax)
	}
	joined := strings.Join(syntaxes, "\n")
	for _, want := range []string{"-p, --prompt <text>", "-h, --help", "-v, --version", "--timeout <duration>", "--raw"} {
		if !strings.Contains(joined, want) {
			t.Errorf("flags lack %q:\n%s", want, joined)
		}
	}
	if strings.Count(joined, "--prompt") != 1 {
		t.Error("-p and --prompt fold into one line")
	}
}
