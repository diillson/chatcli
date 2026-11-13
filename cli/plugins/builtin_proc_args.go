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
			if rest := strings.Join(argv[i:], " "); strings.TrimSpace(rest) != "" {
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
			on := true
			if hasInline {
				on = inline != "false"
			} else if i+1 < len(argv) && (argv[i+1] == "true" || argv[i+1] == "false") {
				i++
				on = argv[i] == "true"
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
