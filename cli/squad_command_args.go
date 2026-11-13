/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * Argument parsing shared by the squad commands (/board, /mail, /agents).
 *
 * Their values are free text typed by a person (titles, descriptions,
 * messages), so the rules differ from a shell: a flag's text value runs
 * until the next flag, apostrophes are always plain characters, and only a
 * double-quoted value can carry a literal "--something".
 */
package cli

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	prompt "github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/i18n"
)

// squadToken is one argument of a squad command line. raw keeps the text as
// typed; quoted marks a double-quoted argument, which is always data and
// never a flag.
type squadToken struct {
	text   string
	raw    string
	quoted bool
}

// tokenizeSquadInput splits on whitespace; a double quote at the start of
// an argument makes everything up to the closing quote one argument.
//
// Only double quotes delimit: an apostrophe that opened a quote paired with
// a possessive further on ("'tis broken --assignee bob for users'") and
// swallowed the flags between them into the title. A double quote inside a
// bare word (5") is text; one left open, or closed mid-word, has no single
// reading and is reported instead of guessed.
func tokenizeSquadInput(input string) ([]squadToken, error) {
	var out []squadToken
	rest := strings.TrimSpace(input)
	for rest != "" {
		if rest[0] != '"' {
			end := strings.IndexFunc(rest, unicode.IsSpace)
			if end < 0 {
				end = len(rest)
			}
			out = append(out, squadToken{text: rest[:end], raw: rest[:end]})
			rest = strings.TrimSpace(rest[end:])
			continue
		}
		end := strings.IndexByte(rest[1:], '"') + 1
		if end == 0 {
			return nil, errors.New(i18n.T("squad.quote.unclosed", truncateForUI(rest, 30)))
		}
		after := rest[end+1:]
		if after != "" && strings.TrimLeftFunc(after, unicode.IsSpace) == after {
			return nil, errors.New(i18n.T("squad.quote.glued", truncateForUI(rest[:end+1], 30)))
		}
		out = append(out, squadToken{text: rest[1:end], raw: rest[:end+1], quoted: true})
		rest = strings.TrimSpace(after)
	}
	return out, nil
}

// isFlag reports whether the token is a flag rather than data.
func (t squadToken) isFlag() bool {
	return !t.quoted && strings.HasPrefix(t.text, "--") && len(t.text) > 2
}

// joinSquadTokens renders a value: one quoted token loses its quotes (they
// only delimited it), anything longer is kept exactly as typed.
func joinSquadTokens(tokens []squadToken) string {
	if len(tokens) == 1 {
		return strings.TrimSpace(tokens[0].text)
	}
	parts := make([]string, len(tokens))
	for i, t := range tokens {
		parts[i] = t.raw
	}
	return strings.Join(parts, " ")
}

// squadFlagKind says how much of the line a flag's value takes.
type squadFlagKind int

const (
	squadFlagBool squadFlagKind = iota // no value
	squadFlagWord                      // exactly the next word
	squadFlagText                      // every word up to the next flag
)

// squadArgs is a parsed squad command tail.
type squadArgs struct {
	pos    []squadToken
	values map[string]string
	set    map[string]bool
}

// positional joins the positionals from index i on.
func (a squadArgs) positional(i int) string {
	if i >= len(a.pos) {
		return ""
	}
	return joinSquadTokens(a.pos[i:])
}

// parseSquadTail parses what follows "/command verb" on a command line.
func parseSquadTail(userInput string, spec map[string]squadFlagKind) (squadArgs, error) {
	tokens, err := tokenizeSquadInput(userInput)
	if err != nil {
		return squadArgs{}, err
	}
	if len(tokens) < 2 {
		tokens = nil
	} else {
		tokens = tokens[2:]
	}
	return parseSquadFlags(tokens, spec)
}

// parseSquadFlags separates positionals from flags. An unknown flag, a flag
// with no value, or a bare word after a flag's one-word value is an error:
// guessing would run the command with something other than what the person
// asked for ("--assignee bob and alice" used to append "and alice" to the
// title).
func parseSquadFlags(tokens []squadToken, spec map[string]squadFlagKind) (squadArgs, error) {
	out := squadArgs{values: map[string]string{}, set: map[string]bool{}}
	valueFlag := "" // last value-taking flag seen
	// "-y" is only a flag where --yes exists; anywhere else it is a word a
	// title or message may contain.
	_, shortYes := spec["--yes"]
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		if !tok.isFlag() && (!shortYes || tok.quoted || tok.text != "-y") {
			if valueFlag != "" {
				return out, errors.New(i18n.T("squad.arg.stray", tok.raw, valueFlag))
			}
			out.pos = append(out.pos, tok)
			continue
		}
		name := tok.text
		if name == "-y" {
			name = "--yes"
		}
		kind, known := spec[name]
		if !known {
			return out, errors.New(i18n.T("squad.flag.unknown", name))
		}
		out.set[name] = true
		if kind == squadFlagBool {
			continue
		}
		valueFlag = name
		var value []squadToken
		for (len(value) == 0 || kind == squadFlagText) && i+1 < len(tokens) && !tokens[i+1].isFlag() {
			i++
			value = append(value, tokens[i])
		}
		if out.values[name] = joinSquadTokens(value); out.values[name] == "" {
			return out, errors.New(i18n.T("squad.flag.missing_value", name))
		}
	}
	return out, nil
}

// printSquadNotice prints one warning line in the squad commands' style.
func printSquadNotice(msg string) {
	fmt.Println(colorize("  "+msg, ColorYellow))
}

// printSquadUsage prints a usage text that may span several lines.
func printSquadUsage(key string) {
	for _, line := range strings.Split(i18n.T(key), "\n") {
		fmt.Println(colorize("  "+line, ColorGray))
	}
}

// squadFlagSuggestions offers the flags of a subcommand that are not on
// the line yet.
func squadFlagSuggestions(args []string, current string, flags ...prompt.Suggest) []prompt.Suggest {
	used := make(map[string]bool, len(args))
	for _, a := range args {
		used[a] = true
	}
	var out []prompt.Suggest
	for _, f := range flags {
		if !used[f.Text] || f.Text == current {
			out = append(out, f)
		}
	}
	return prompt.FilterHasPrefix(out, current, true)
}
