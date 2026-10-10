/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/diillson/chatcli/i18n"
)

// Subcommand describes one top-level `chatcli <name>` entry point. This list
// is the single source for `chatcli --help`, each subcommand's generic
// `--help`, and the subcommand section of /help; a parity test holds it
// equal to the names main.go dispatches.
type Subcommand struct {
	Name    string
	Aliases []string
	// Usage is the literal syntax line (CLI tokens are never translated).
	Usage string
	// SummaryKey is the i18n key of the one-line description.
	SummaryKey string
	// OwnHelp marks subcommands that answer -h/--help themselves (their
	// own usage text or flag set); the rest get the generic usage below.
	OwnHelp bool
}

// Summary resolves the localized one-line description.
func (s Subcommand) Summary() string { return i18n.T(s.SummaryKey) }

var subcommands = []Subcommand{
	{Name: "eval", Usage: "chatcli eval run|list|validate|compare [suite] [flags]", SummaryKey: "subcmd.eval", OwnHelp: true},
	{Name: "tool", Usage: "chatcli tool [list | <@tool> [args…]]", SummaryKey: "subcmd.tool"},
	{Name: "storage", Usage: "chatcli storage [store] | chatcli storage prune [store] [--apply]", SummaryKey: "subcmd.storage", OwnHelp: true},
	{Name: "update", Usage: "chatcli update [check]", SummaryKey: "subcmd.update", OwnHelp: true},
	{Name: "dash", Usage: "chatcli dash", SummaryKey: "subcmd.dash"},
	{Name: "web", Usage: "chatcli web [flags]", SummaryKey: "subcmd.web", OwnHelp: true},
	{Name: "server", Aliases: []string{"serve"}, Usage: "chatcli server [flags]", SummaryKey: "subcmd.server", OwnHelp: true},
	{Name: "healthcheck", Usage: "chatcli healthcheck [flags]", SummaryKey: "subcmd.healthcheck", OwnHelp: true},
	{Name: "connect", Usage: "chatcli connect [flags] [address]", SummaryKey: "subcmd.connect", OwnHelp: true},
	{Name: "watch", Usage: "chatcli watch [flags]", SummaryKey: "subcmd.watch", OwnHelp: true},
	{Name: "gateway", Usage: "chatcli gateway", SummaryKey: "subcmd.gateway"},
	{Name: "daemon", Usage: "chatcli daemon start|stop|status|ping|install [--socket path] [--detach]", SummaryKey: "subcmd.daemon", OwnHelp: true},
	{Name: "mcp-server", Aliases: []string{"mcp-serve"}, Usage: "chatcli mcp-server", SummaryKey: "subcmd.mcp_server"},
	{Name: "acp", Usage: "chatcli acp", SummaryKey: "subcmd.acp"},
	{Name: "mcp", Usage: "chatcli mcp add|list|get|remove …", SummaryKey: "subcmd.mcp", OwnHelp: true},
	{Name: "plugin", Usage: "chatcli plugin keygen|sign|verify|trust|quarantine …", SummaryKey: "subcmd.plugin", OwnHelp: true},
}

// Subcommands returns the top-level subcommand registry.
func Subcommands() []Subcommand { return subcommands }

// LookupSubcommand resolves a name or alias.
func LookupSubcommand(name string) (Subcommand, bool) {
	for _, s := range subcommands {
		if s.Name == name {
			return s, true
		}
		for _, a := range s.Aliases {
			if a == name {
				return s, true
			}
		}
	}
	return Subcommand{}, false
}

// IsHelpArg reports whether args ask for help: the first argument is
// -h, --help or help.
func IsHelpArg(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "-h", "-help", "--help", "help":
		return true
	}
	return false
}

// PrintSubcommandUsage prints the generic help of a subcommand that has no
// usage text of its own.
func PrintSubcommandUsage(w io.Writer, s Subcommand) {
	fmt.Fprintf(w, "%s\n\n  %s\n", s.Summary(), s.Usage)
	if len(s.Aliases) > 0 {
		fmt.Fprintf(w, "\n%s %s\n", i18n.T("help.usage.aliases"), strings.Join(s.Aliases, ", "))
	}
}

// PrintUsage prints `chatcli --help`: invocation forms, every one-shot flag
// (from the live flag set) and every subcommand (from the registry).
func PrintUsage(w io.Writer) {
	fmt.Fprintln(w, i18n.T("help.usage.title"))
	fmt.Fprintln(w)
	fmt.Fprintln(w, i18n.T("help.usage.forms"))
	fmt.Fprintln(w)
	fmt.Fprintln(w, i18n.T("help.usage.flags"))
	for _, f := range OneShotFlags() {
		fmt.Fprintf(w, "  %-28s %s\n", f.Syntax, f.Usage)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, i18n.T("help.usage.subcommands"))
	for _, s := range subcommands {
		name := s.Name
		if len(s.Aliases) > 0 {
			name += " (" + strings.Join(s.Aliases, ", ") + ")"
		}
		fmt.Fprintf(w, "  %-28s %s\n", name, s.Summary())
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, i18n.T("help.usage.footer"))
}

// FlagHelp is one one-shot flag as help renders it.
type FlagHelp struct {
	Syntax string // "-p, --prompt <text>"
	Usage  string
}

// OneShotFlags lists the top-level flags from the live flag set, aliases
// folded together (-p/--prompt share one line). Built on each call so the
// descriptions resolve in the active language.
func OneShotFlags() []FlagHelp {
	fs, _ := NewFlagSet()
	type group struct {
		names []string
		usage string
		arg   string
	}
	byUsage := map[string]*group{}
	var order []*group
	fs.VisitAll(func(f *flag.Flag) {
		g, ok := byUsage[f.Usage]
		if !ok {
			g = &group{usage: f.Usage, arg: flagArg(f)}
			byUsage[f.Usage] = g
			order = append(order, g)
		}
		g.names = append(g.names, f.Name)
	})
	out := make([]FlagHelp, 0, len(order))
	for _, g := range order {
		sort.Slice(g.names, func(i, j int) bool { return len(g.names[i]) < len(g.names[j]) })
		parts := make([]string, len(g.names))
		for i, n := range g.names {
			if len(n) == 1 {
				parts[i] = "-" + n
			} else {
				parts[i] = "--" + n
			}
		}
		syntax := strings.Join(parts, ", ")
		if g.arg != "" {
			syntax += " " + g.arg
		}
		out = append(out, FlagHelp{Syntax: syntax, Usage: g.usage})
	}
	// Alphabetical by long name, whatever the short alias.
	key := func(f FlagHelp) string {
		name := strings.Fields(f.Syntax)[0]
		if i := strings.LastIndex(f.Syntax, "--"); i >= 0 {
			name = strings.Fields(f.Syntax[i:])[0]
		}
		return strings.TrimLeft(strings.TrimSuffix(name, ","), "-")
	}
	sort.SliceStable(out, func(i, j int) bool { return key(out[i]) < key(out[j]) })
	return out
}

// flagArg names a flag's value placeholder from its type.
func flagArg(f *flag.Flag) string {
	if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
		return ""
	}
	switch f.Name {
	case "p", "prompt":
		return "<text>"
	case "timeout":
		return "<duration>"
	case "max-tokens":
		return "<n>"
	}
	return "<value>"
}
