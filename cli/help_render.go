/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/diillson/chatcli/cli/palette"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/ui/kit"
)

// helpNameWidth is the command column of the help listing.
const helpNameWidth = 30

// helpCategoryOrder is the section order of /help; it follows the palette's
// categories, so a command lands in the same section on both surfaces.
var helpCategoryOrder = []palette.Category{
	palette.CatCore, palette.CatModel, palette.CatSession, palette.CatContext,
	palette.CatAgent, palette.CatQuality, palette.CatIntegrations,
	palette.CatScheduler, palette.CatSystem, palette.CatCommands,
}

// helpStyle colors the help output, or leaves it plain (the LLM-facing
// catalog and tests).
type helpStyle struct{ color bool }

func (s helpStyle) paint(text, color string) string {
	if !s.color {
		return text
	}
	return colorize(text, color)
}

func (s helpStyle) section(w io.Writer, title string) {
	fmt.Fprintf(w, "\n  %s\n", s.paint(title, ColorLime))
}

func (s helpStyle) row(w io.Writer, name, desc string) {
	fmt.Fprintf(w, "    %s  %s\n", s.paint(kit.PadRight(name, helpNameWidth), ColorCyan), s.paint(desc, ColorGray))
}

func (s helpStyle) note(w io.Writer, text string) {
	fmt.Fprintf(w, "    %s\n", s.paint(text, ColorGray))
}

// handleHelpCommand serves /help and /help <command>.
func (cli *ChatCLI) handleHelpCommand(input string) {
	arg := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(input), "/help"))
	style := helpStyle{color: true}
	if arg == "" {
		cli.renderHelp(os.Stdout, style)
		return
	}
	if !cli.renderCommandHelp(os.Stdout, style, arg) {
		fmt.Println(i18n.T("help.unknown_command", arg))
	}
}

// renderHelp writes the full /help: every root command from the registry,
// grouped by category with its aliases, then the non-slash surfaces
// (context modifiers, agent keys, one-shot flags, subcommands, tips).
// Generated, never hand-listed: a command added to the registry appears
// here without touching this function.
func (cli *ChatCLI) renderHelp(w io.Writer, s helpStyle) {
	fmt.Fprintln(w, "\n"+s.paint(i18n.T("help.header.title"), ColorBold))
	fmt.Fprintln(w, s.paint(i18n.T("help.header.subtitle1"), ColorGray))
	fmt.Fprintln(w, s.paint(i18n.T("help.header.subtitle2"), ColorGray))

	byCat := map[palette.Category][]palette.RootCommand{}
	for _, rc := range palette.AllRootCommands() {
		byCat[rc.Category] = append(byCat[rc.Category], rc)
	}
	for _, cat := range helpCategoryOrder {
		cmds := byCat[cat]
		if len(cmds) == 0 {
			continue
		}
		s.section(w, cat.Label())
		for _, rc := range cmds {
			name := rc.Name
			if aliases := palette.Aliases(rc.Name); len(aliases) > 0 {
				name += " | " + strings.Join(aliases, " | ")
			}
			s.row(w, name, rc.Summary())
		}
	}

	s.section(w, i18n.T("help.section.context"))
	s.row(w, "@file <path> [--mode …]", i18n.T("help.command.file"))
	s.row(w, "  --mode <mode>", i18n.T("help.command.file_modes"))
	s.row(w, "@git", i18n.T("help.command.git"))
	s.row(w, "@history", i18n.T("help.command.history"))
	s.row(w, "@env", i18n.T("help.command.env"))
	s.row(w, "@command <cmd>", i18n.T("help.command.command"))
	s.row(w, "@command -i <cmd>", i18n.T("help.command.command_i"))
	s.row(w, "@command --ai <cmd> [> text]", i18n.T("help.command.command_ai"))

	s.section(w, i18n.T("help.section.agent_keys"))
	for _, k := range [][2]string{
		{"[1..N]", "help.command.agent_exec_n"}, {"a", "help.command.agent_exec_all"},
		{"eN", "help.command.agent_edit"}, {"tN", "help.command.agent_dry_run"},
		{"cN", "help.command.agent_continue"}, {"pcN", "help.command.agent_pre_context"},
		{"acN", "help.command.agent_post_context"}, {"vN", "help.command.agent_view"},
		{"wN", "help.command.agent_save"}, {"p", "help.command.agent_toggle_plan"},
		{"r", "help.command.agent_redraw"}, {"q", "help.command.agent_quit"},
	} {
		s.row(w, k[0], i18n.T(k[1]))
	}
	s.note(w, i18n.T("help.command.agent_side_commands", strings.Join(sideCommandRoots, " ")))

	s.section(w, i18n.T("help.section.oneshot"))
	for _, f := range OneShotFlags() {
		s.row(w, f.Syntax, f.Usage)
	}
	s.note(w, i18n.T("help.command.oneshot_pipes"))

	s.section(w, i18n.T("help.section.subcommands"))
	for _, sc := range Subcommands() {
		name := "chatcli " + sc.Name
		if len(sc.Aliases) > 0 {
			name += " | " + strings.Join(sc.Aliases, " | ")
		}
		s.row(w, name, sc.Summary())
	}

	s.section(w, i18n.T("help.section.tips"))
	for _, key := range []string{"help.command.tips_cancel", "help.command.tips_exit", "help.command.tips_rewind", "help.command.tips_operator"} {
		s.note(w, i18n.T(key))
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, s.paint(i18n.T("help.footer"), ColorGray))
	fmt.Fprintln(w)
}

// renderCommandHelp writes /help <command>: the command's summary, aliases
// and its subcommands/flags as the live completer offers them — the same
// source the inline completion and the palette use, so it is never stale.
// It returns false for a name that is not a root command.
func (cli *ChatCLI) renderCommandHelp(w io.Writer, s helpStyle, arg string) bool {
	name := strings.Fields(arg)[0]
	if !strings.HasPrefix(name, "/") {
		name = "/" + name
	}
	name = palette.Canonical(strings.ToLower(name))
	summary, ok := palette.RootSummary(name)
	if !ok {
		return false
	}
	fmt.Fprintf(w, "\n  %s  %s\n", s.paint(name, ColorCyan+ColorBold), summary)
	if aliases := palette.Aliases(name); len(aliases) > 0 {
		s.note(w, i18n.T("help.usage.aliases")+" "+strings.Join(aliases, ", "))
	}
	var opts []palette.Suggestion
	for _, sg := range cli.paletteSuggest(name + " ") {
		if strings.TrimSpace(sg.Text) != "" {
			opts = append(opts, sg)
		}
	}
	if len(opts) == 0 {
		s.note(w, i18n.T("help.command_no_options"))
		fmt.Fprintln(w)
		return true
	}
	s.section(w, i18n.T("help.command_options"))
	for _, o := range opts {
		s.row(w, o.Text, o.Desc)
	}
	fmt.Fprintln(w)
	return true
}
