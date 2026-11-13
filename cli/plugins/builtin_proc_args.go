/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * builtin_proc_args.go — the arguments of each @proc subcommand, read from
 * the argv the agent loop delivers (the flattened {cmd,args} envelope) and
 * from the envelope itself. The tool used to hand argv to the generic flag
 * reader and unmarshal whatever came out, which ignored every key it did
 * not know: a mistyped --tial silently tailed the default, and a start
 * with a mistyped --dri ran in the wrong directory. It also took any token
 * beginning with dashes for a flag, so "start npm run dev -- --port 3000"
 * lost the child's arguments and stdin could not type "--help".
 */
package plugins

import (
	"encoding/json"
	"fmt"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// procArgKind is how the value of one @proc argument is read from argv.
type procArgKind int

const (
	// procArgText is free text (a command line, text to type, a regexp): it
	// may itself begin with dashes, so the next token is the value whatever
	// it looks like.
	procArgText procArgKind = iota
	// procArgWord is an id or a path: a next token that is a flag means the
	// value is missing.
	procArgWord
	// procArgInt is a whole number.
	procArgInt
	// procArgBool is a bare flag (the flattener drops false and writes true
	// as the bare flag); an explicit true/false after it is accepted.
	procArgBool
)

// procArgs lists, per subcommand, exactly the arguments Execute reads.
var procArgs = map[string]map[string]procArgKind{
	"start":  {"command": procArgText, "dir": procArgWord, "pty": procArgBool},
	"stdin":  {"id": procArgWord, "text": procArgText, "no_enter": procArgBool},
	"status": {"id": procArgWord},
	"wait":   {"id": procArgWord, "pattern": procArgText, "timeout": procArgInt},
	"logs":   {"id": procArgWord, "tail": procArgInt, "grep": procArgText},
	"stop":   {"id": procArgWord},
	"remove": {"id": procArgWord},
	"list":   {},
}

// procAccepted renders the arguments of sub for an error message.
func procAccepted(sub string) string {
	names := make([]string, 0, len(procArgs[sub]))
	for name := range procArgs[sub] {
		names = append(names, "--"+name)
	}
	if len(names) == 0 {
		return "it takes no arguments"
	}
	sort.Strings(names)
	return "accepted: " + strings.Join(names, ", ")
}

// procArgName folds the spellings of an argument name onto the canonical
// one: dashes for underscores, and "cmd" for start's command (the agent
// loop's flattener writes the command under that key).
func procArgName(sub, name string) string {
	name = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(name)), "-", "_")
	if sub == "start" && name == "cmd" {
		return "command"
	}
	return name
}

// procArgvToInner reads the argv after the subcommand into the inner-args
// JSON. Tool flags are read until the positional part begins: for start
// that is the command line, and everything from its first word on belongs
// to the child, dashes included; for the other verbs it is the process id.
// An unknown flag, a flag without its value, a repeated flag, a number that
// is not one and a stray positional are errors.
func procArgvToInner(sub string, argv []string) (string, error) {
	spec := procArgs[sub]
	primary := "id"
	if sub == "start" {
		primary = "command"
	}
	in := map[string]any{}
	for i := 0; i < len(argv); i++ {
		tok := argv[i]
		if !strings.HasPrefix(tok, "--") || tok == "--" {
			if _, takes := spec[primary]; !takes {
				return "", fmt.Errorf("%s: unexpected argument %q (%s)", sub, tok, procAccepted(sub))
			}
			if _, set := in[primary]; set {
				return "", fmt.Errorf("%s: unexpected argument %q — %s was already given", sub, tok, primary)
			}
			if sub != "start" {
				in[primary] = tok
				continue
			}
			if tok == "--" {
				i++
			}
			if rest := procCommandLine(argv[i:]); strings.TrimSpace(rest) != "" {
				in[primary] = rest
			}
			break
		}
		name, inline, hasInline := strings.Cut(strings.TrimLeft(tok, "-"), "=")
		name = procArgName(sub, name)
		kind, known := spec[name]
		if !known {
			return "", fmt.Errorf("%s: unknown argument %q (%s)", sub, tok, procAccepted(sub))
		}
		if _, dup := in[name]; dup {
			return "", fmt.Errorf("%s: --%s given more than once", sub, name)
		}
		if kind == procArgBool {
			// Bare, or --flag=value. The word AFTER a bare flag is never its
			// value: "true" and "false" are programs, and `start --pty true`
			// starts one.
			on := true
			if hasInline {
				switch strings.ToLower(inline) {
				case "true", "1":
				case "false", "0":
					on = false
				default:
					return "", fmt.Errorf("%s: --%s is a bare flag; an explicit value must be --%s=true or --%s=false, got %q", sub, name, name, name, inline)
				}
			}
			in[name] = on
			continue
		}
		value := inline
		if !hasInline {
			if i+1 >= len(argv) || (kind != procArgText && strings.HasPrefix(argv[i+1], "--")) {
				return "", fmt.Errorf("%s: --%s needs a value (%s)", sub, name, procAccepted(sub))
			}
			i++
			value = argv[i]
		}
		if kind != procArgInt {
			in[name] = value
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return "", fmt.Errorf("%s: --%s must be a whole number, got %q", sub, name, value)
		}
		in[name] = n
	}
	b, err := json.Marshal(in)
	return string(b), err
}

// procCommandLine turns the positional words of a start into the line the
// shell runs. The words were separate arguments where they were typed, and
// joined bare they split again (`sh -c "echo hi; sleep 5"` became
// `sh -c echo hi; sleep 5`), so a later word that the shell would re-read
// is quoted. This is a best effort over argv; a line with real shell
// syntax belongs in the command value, which is passed as it is.
func procCommandLine(words []string) string {
	out := make([]string, len(words))
	for i, w := range words {
		out[i] = w
		// The first word is the command, or a whole fragment of a command
		// line ("npm run dev", "FOO=1 ./run.sh"): quoting it would make the
		// shell look for a program of that full name.
		if i > 0 && procNeedsQuoting(w) {
			out[i] = procShellQuote(w)
		}
	}
	return strings.Join(out, " ")
}

// procShellOperators are the words that are shell syntax when they stand
// alone between the words of a command line.
var procShellOperators = map[string]bool{
	"&&": true, "||": true, "|": true, "|&": true, ";": true, "&": true,
	">": true, ">>": true, "<": true, "<<": true, "2>": true, "2>>": true,
	"&>": true, "&>>": true, "2>&1": true, "1>&2": true, ">&2": true, ">&1": true,
	"(": true, ")": true, "{": true, "}": true, "!": true,
}

// procNeedsQuoting reports whether a later positional word must be quoted
// to reach the child as the one argument it was typed as: it is empty, or
// carries whitespace, a quote, a backtick, a command substitution or a
// command separator. A word that is itself an operator, a plain $VAR or a
// glob is what the user meant the shell to read, and stays.
func procNeedsQuoting(w string) bool {
	if procShellOperators[w] {
		return false
	}
	return w == "" || strings.ContainsAny(w, " \t\n\r'\"`;") || strings.Contains(w, "$(")
}

// procShellQuote quotes one argument for the platform shell the supervisor
// runs commands with: single quotes for sh, double quotes for cmd.exe.
func procShellQuote(arg string) string {
	if runtime.GOOS == "windows" {
		return `"` + strings.ReplaceAll(arg, `"`, `""`) + `"`
	}
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

// procCheckEnvelope applies the same rule to the args object of a JSON
// envelope: names are folded to the canonical ones and a key the
// subcommand does not read is an error.
func procCheckEnvelope(sub, inner string) (string, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(inner), &raw); err != nil {
		return inner, nil // not an object: Execute reports the parse error
	}
	out := make(map[string]json.RawMessage, len(raw))
	for key, value := range raw {
		name := procArgName(sub, key)
		if _, known := procArgs[sub][name]; !known {
			return "", fmt.Errorf("%s: unknown argument %q (%s)", sub, key, procAccepted(sub))
		}
		if _, dup := out[name]; dup {
			return "", fmt.Errorf("%s: %s given more than once", sub, name)
		}
		out[name] = value
	}
	b, err := json.Marshal(out)
	return string(b), err
}
