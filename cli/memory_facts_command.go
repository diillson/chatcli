/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	prompt "github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/i18n"
)

// Shapes a slash-command flag can take in parseSlashFlags.
const (
	slashFlagBool   = iota // bare flag, consumes nothing
	slashFlagToken         // exactly one following token
	slashFlagGreedy        // every following token up to the next flag
)

// slashTokenRe finds the whitespace-separated tokens of a command tail with
// their offsets, so text can be cut from the line as it was typed.
var slashTokenRe = regexp.MustCompile(`\S+`)

// parseSlashFlags splits the free-form tail of a slash command into its
// positional text and the flags named in spec. An unknown flag, one missing
// its value, or a value flag given twice is an error: read as text, or with
// the last value winning, the command would act on something other than
// what was typed ("forget --id A --id B" removing only B). A lone "--" ends
// the flags, which is how a text that starts with "--" is passed. Text and
// values are cut from the original line so inner spacing is kept.
func parseSlashFlags(rest string, spec map[string]int) (string, map[string]string, error) {
	spans := slashTokenRe.FindAllStringIndex(rest, -1)
	flags := map[string]string{}
	var text []string
	textEnd := -1 // index in spans of the last positional token, -1 = none adjacent
	for i := 0; i < len(spans); i++ {
		tok := rest[spans[i][0]:spans[i][1]]
		if tok == "--" {
			if tail := strings.TrimSpace(rest[spans[i][1]:]); tail != "" {
				text = append(text, tail)
			}
			break
		}
		if !strings.HasPrefix(tok, "--") {
			if textEnd == i-1 && len(text) > 0 {
				// Adjacent to the previous word: keep the spacing between them.
				text[len(text)-1] += rest[spans[i-1][1]:spans[i][1]]
			} else {
				text = append(text, tok)
			}
			textEnd = i
			continue
		}
		shape, known := spec[tok]
		if !known {
			return "", nil, errors.New(i18n.T("kp.flag.unknown_or_text", tok))
		}
		if shape == slashFlagBool {
			flags[tok] = "true"
			continue
		}
		if _, repeated := flags[tok]; repeated {
			return "", nil, errors.New(i18n.T("kp.flag.repeated", tok))
		}
		first := i + 1
		for i+1 < len(spans) && !strings.HasPrefix(rest[spans[i+1][0]:spans[i+1][1]], "--") {
			i++
			if shape == slashFlagToken {
				break
			}
		}
		if i < first {
			return "", nil, errors.New(i18n.T("kp.flag.missing_value", tok))
		}
		flags[tok] = rest[spans[first][0]:spans[i][1]]
	}
	return strings.Join(text, " "), flags, nil
}

// forgetMemoryCommand backs /memory forget: every fact containing a text, a
// preview of that removal (--dry-run), or exactly one fact by id (--id) —
// the same three forms @memory forget has.
func (cli *ChatCLI) forgetMemoryCommand(rest string) {
	text, flags, err := parseSlashFlags(rest, map[string]int{"--id": slashFlagToken, "--dry-run": slashFlagBool})
	if err != nil {
		fmt.Println(colorize("  "+err.Error()+" "+i18n.T("kp.memory.forget.usage"), ColorYellow))
		return
	}
	id, byID := flags["--id"]
	_, dryRun := flags["--dry-run"]
	switch {
	case byID && (text != "" || dryRun):
		fmt.Println(colorize("  "+i18n.T("kp.memory.forget.id_exclusive"), ColorYellow))
	case byID:
		cli.forgetMemoryFactByID(id)
	case text == "":
		fmt.Println(colorize("  "+i18n.T("kp.memory.forget.usage"), ColorGray))
	case dryRun:
		cli.previewForgetMemoryFacts(text)
	default:
		cli.forgetMemoryFact(text)
	}
}

func (cli *ChatCLI) forgetMemoryFactByID(id string) {
	f, n := cli.memoryStore.Manager().ForgetFactByID(id)
	switch {
	case f != nil:
		if cli.contextBuilder != nil {
			cli.contextBuilder.InvalidateCache()
		}
		fmt.Printf("  %s %s\n", colorize("OK", ColorGreen), i18n.T("mem.tool.forget.byid", shortFactID(f.ID), f.Content))
	case n == 0:
		fmt.Printf("  %s %s\n", colorize("!", ColorYellow), i18n.T("kp.memory.forget.id_none", id))
	default:
		fmt.Printf("  %s %s\n", colorize("!", ColorYellow), i18n.T("kp.memory.forget.id_ambiguous", n, id))
	}
}

func (cli *ChatCLI) previewForgetMemoryFacts(text string) {
	facts := cli.memoryStore.Manager().FactsMatching(text)
	if len(facts) == 0 {
		fmt.Printf("  %s %s\n", colorize("!", ColorYellow), i18n.T("mem.tool.forget.preview_none", text))
		return
	}
	fmt.Println(colorize("  "+i18n.T("kp.memory.forget.preview", len(facts), text), ColorCyan))
	for _, line := range strings.Split(renderFactLines(facts), "\n") {
		fmt.Println("  " + line)
	}
}

// parseFactsListing reads the tail of /memory facts [category]
// [--match <text>] [--limit <n>]. match comes back lowercased; limit 0
// means no cap. It prints the problem and reports false when the tail does
// not parse — a bad --limit must not silently list everything.
func parseFactsListing(rest string) (category, match string, limit int, ok bool) {
	category, flags, err := parseSlashFlags(rest, map[string]int{"--match": slashFlagGreedy, "--limit": slashFlagToken})
	if err != nil {
		fmt.Println(colorize("  "+err.Error()+" "+i18n.T("kp.memory.facts.usage"), ColorYellow))
		return "", "", 0, false
	}
	if raw, has := flags["--limit"]; has {
		if limit, err = strconv.Atoi(raw); err != nil || limit <= 0 {
			fmt.Println(colorize("  "+i18n.T("kp.memory.facts.bad_limit", raw), ColorYellow))
			return "", "", 0, false
		}
	}
	return category, strings.ToLower(flags["--match"]), limit, true
}

// memoryFactArgSuggestions completes /memory facts and /memory forget:
// categories in the first slot of facts and the flags of each.
func (cli *ChatCLI) memoryFactArgSuggestions(args []string, line, word string) []prompt.Suggest {
	prev := args[len(args)-1]
	if !strings.HasSuffix(line, " ") {
		prev = args[len(args)-2]
	}
	var out []prompt.Suggest
	switch {
	case prev == "--id" || prev == "--match" || prev == "--limit":
		return nil
	case args[1] == "forget":
		out = []prompt.Suggest{
			{Text: "--dry-run", Description: i18n.T("kp.complete.memory.forget_dry_run")},
			{Text: "--id", Description: i18n.T("kp.complete.memory.forget_id")},
		}
	default:
		if atArg(args, line, 2) {
			out = memoryCategorySuggestions(false)
		}
		out = append(out,
			prompt.Suggest{Text: "--match", Description: i18n.T("kp.complete.memory.facts_match")},
			prompt.Suggest{Text: "--limit", Description: i18n.T("kp.complete.memory.facts_limit")},
		)
	}
	return prompt.FilterHasPrefix(out, word, true)
}
