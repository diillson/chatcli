/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * memory_graph_command.go — the vault-model subcommands of /memory:
 *
 *   /memory link <from> -> <to> [--rel <rel>] [--note <text>]
 *   /memory unlink <from> -> <to> [--rel <rel>]
 *   /memory links <note>
 *   /memory vault [status|sync [--force]|path]
 *
 * Note references are the same everywhere (@memory link, wikilinks): a node
 * id, a fact id or prefix, a title or alias, or [[a wikilink]].
 */
package cli

import (
	"fmt"
	"strings"
	"sync"
	"time"

	prompt "github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/knowledge"
	"github.com/diillson/chatcli/pkg/vault"
)

// linkArgs is a parsed /memory link|unlink argument line.
type linkArgs struct {
	from, to, rel, note string
}

// parseLinkArgs reads "<from> -> <to> [--rel r] [--note text]". The arrow
// may also be "→"; quoted endpoints ("a b" "c d") work without an arrow.
func parseLinkArgs(s string) (linkArgs, bool) {
	var out linkArgs
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "--note "); i >= 0 {
		out.note = strings.TrimSpace(s[i+len("--note "):])
		s = strings.TrimSpace(s[:i])
	}
	if i := strings.Index(s, "--rel "); i >= 0 {
		rest := strings.TrimSpace(s[i+len("--rel "):])
		if f := strings.Fields(rest); len(f) > 0 {
			out.rel = f[0]
		}
		s = strings.TrimSpace(s[:i])
	}
	for _, arrow := range []string{"->", "→"} {
		if a, b, ok := strings.Cut(s, arrow); ok {
			out.from, out.to = unquote(a), unquote(b)
			return out, out.from != "" && out.to != ""
		}
	}
	if parts := quotedFields(s); len(parts) == 2 {
		out.from, out.to = parts[0], parts[1]
		return out, true
	}
	return out, false
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		s = s[1 : len(s)-1]
	}
	return strings.TrimSpace(s)
}

// quotedFields splits on spaces, keeping "double" or 'single' quoted runs.
func quotedFields(s string) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case quote != 0 && r == quote:
			quote = 0
			flush()
		case quote != 0:
			cur.WriteRune(r)
		case r == '"' || r == '\'':
			flush()
			quote = r
		case r == ' ' || r == '\t':
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

func (cli *ChatCLI) memoryLinkCommand(args string, remove bool) {
	la, ok := parseLinkArgs(args)
	if !ok {
		key := "memory.link.usage"
		if remove {
			key = "memory.unlink.usage"
		}
		fmt.Println(colorize("  "+i18n.T(key), ColorGray))
		return
	}
	var (
		out string
		err error
	)
	if remove {
		out, err = cli.graphUnlinkText(la.from, la.to, la.rel)
	} else {
		out, err = cli.graphLinkText(la.from, la.to, la.rel, la.note)
	}
	if err != nil {
		fmt.Println(colorize("  "+err.Error(), ColorYellow))
		return
	}
	fmt.Println(colorize("  "+out, ColorGreen))
}

func (cli *ChatCLI) memoryLinksCommand(ref string) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		fmt.Println(colorize("  "+i18n.T("memory.links.usage"), ColorGray))
		return
	}
	out, err := cli.graphNeighborsText(ref)
	if err != nil {
		fmt.Println(colorize("  "+err.Error(), ColorYellow))
		return
	}
	fmt.Println()
	for _, line := range strings.Split(out, "\n") {
		fmt.Println("  " + line)
	}
	fmt.Println()
}

func (cli *ChatCLI) memoryVaultCommand(args string) {
	f := strings.Fields(args)
	sub := "status"
	if len(f) > 0 {
		sub = strings.ToLower(f[0])
	}
	dir := cli.vaultDir()
	current := cli.currentVaultSyncer()
	switch sub {
	case "path":
		fmt.Println("  " + dir)
	case "sync":
		force := len(f) > 1 && (f[1] == "--force" || f[1] == "-f")
		s := current
		if s == nil {
			// Generation is off: an explicit sync still works, once.
			s = newVaultSyncer(cli.graphSources(), dir, cli.logger)
		}
		fmt.Println(colorize("  "+i18n.T("memory.vault.syncing", dir), ColorGray))
		rep, err := s.run(force)
		printVaultReport(rep, err, time.Now())
	case "status":
		fmt.Println(colorize("  "+i18n.T("memory.vault.dir", dir), ColorCyan))
		if !vaultEnabled() {
			fmt.Println(colorize("  "+i18n.T("memory.vault.disabled"), ColorYellow))
		}
		if current == nil {
			return
		}
		rep, at, err := current.status()
		if at.IsZero() {
			fmt.Println(colorize("  "+i18n.T("memory.vault.never"), ColorGray))
			return
		}
		printVaultReport(rep, err, at)
	default:
		fmt.Println(colorize("  "+i18n.T("memory.vault.usage"), ColorGray))
	}
}

func printVaultReport(rep vault.Report, err error, at time.Time) {
	fmt.Println(colorize("  "+i18n.T("memory.vault.report", at.Format("2006-01-02 15:04:05"),
		rep.Written, rep.Unchanged, rep.Removed, len(rep.Kept), rep.Duration.Round(time.Millisecond).String()), ColorGreen))
	if len(rep.Kept) > 0 {
		fmt.Println(colorize("  "+i18n.T("memory.vault.kept_hint"), ColorGray))
		for i, k := range rep.Kept {
			if i >= 10 {
				fmt.Println(colorize("    "+i18n.T("memory.vault.more", len(rep.Kept)-10), ColorGray))
				break
			}
			fmt.Println(colorize("    - "+k, ColorGray))
		}
	}
	if err != nil {
		fmt.Println(colorize("  "+i18n.T("memory.vault.error", err.Error()), ColorYellow))
	}
}

// graphHubSuggestions offers the graph's hub notes as completions for the
// note argument of /memory links|link|unlink — node ids, because they are
// single tokens the completer can insert. It reads the cached snapshot only
// (a keystroke never pays a rebuild) and memoizes per snapshot, so typing
// does not re-rank the graph.
func (cli *ChatCLI) graphHubSuggestions() []prompt.Suggest {
	if cli.memoryStore == nil {
		return nil
	}
	g := cli.memoryStore.Manager().PeekKnowledgeGraph()
	if g == nil {
		return nil
	}
	hubSuggestMemo.mu.Lock()
	defer hubSuggestMemo.mu.Unlock()
	if hubSuggestMemo.graph == g {
		return hubSuggestMemo.out
	}
	var out []prompt.Suggest
	for _, h := range g.Hubs(graphHubSuggestionLimit) {
		if h.Kind == knowledge.KindTag {
			continue
		}
		out = append(out, prompt.Suggest{Text: h.ID, Description: describeNode(h)})
	}
	hubSuggestMemo.graph, hubSuggestMemo.out = g, out
	return out
}

// hubSuggestMemo caches the hub completions of the last snapshot seen.
var hubSuggestMemo struct {
	mu    sync.Mutex
	graph *knowledge.Graph
	out   []prompt.Suggest
}

// graphHubSuggestionLimit bounds the note completions offered.
const graphHubSuggestionLimit = 20
