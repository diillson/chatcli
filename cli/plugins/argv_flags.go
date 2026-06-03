/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * Shared argv→JSON parsing for builtin tools.
 *
 * The agent flattens a {cmd, args:{...}} tool envelope into a flag-style argv
 * before dispatch (see buildArgvFromJSONMap): each "--key value" pair, with
 * array fields emitted as repeated "--key value" pairs and booleans as a bare
 * "--key". Builtin plugins therefore receive, after the subcommand token,
 * something like:
 *
 *   ["create", "--name", "deploy-x", "--triggers", "a", "--triggers", "b", ...]
 *
 * argvToInnerJSON turns that tail into the inner-args JSON object the plugins
 * unmarshal, so a single code path handles both the JSON envelope and the
 * flattened argv form. Keys listed in arrayKeys are always emitted as JSON
 * arrays (so a single "--triggers a" still unmarshals into []string).
 */
package plugins

import (
	"encoding/json"
	"strconv"
	"strings"
)

// argvToInnerJSON converts the argv tail (everything AFTER the subcommand) into
// (positional, innerJSON). positional is any leading bare token(s) before the
// first flag (joined by spaces); innerJSON is the {key:value|[...]} object built
// from the --flag pairs.
func argvToInnerJSON(argv []string, arrayKeys, intKeys map[string]bool) (positional, innerJSON string) {
	values := map[string][]string{}
	var order []string
	var pos []string
	seenFlag := false

	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if strings.HasPrefix(a, "--") {
			seenFlag = true
			key := strings.TrimLeft(a, "-")
			if _, ok := values[key]; !ok {
				order = append(order, key)
			}
			if i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "--") {
				values[key] = append(values[key], argv[i+1])
				i++
			} else {
				values[key] = append(values[key], "\x00bool") // sentinel for bare flag
			}
			continue
		}
		if !seenFlag {
			pos = append(pos, a)
		}
	}

	obj := map[string]interface{}{}
	for _, k := range order {
		vs := values[k]
		switch {
		case arrayKeys[k] || len(vs) > 1:
			obj[k] = vs
		case len(vs) == 1 && vs[0] == "\x00bool":
			obj[k] = true
		case len(vs) == 1 && intKeys[k]:
			if n, err := strconv.Atoi(strings.TrimSpace(vs[0])); err == nil {
				obj[k] = n
			} else {
				obj[k] = vs[0]
			}
		case len(vs) == 1:
			obj[k] = vs[0]
		}
	}
	b, _ := json.Marshal(obj)
	return strings.Join(pos, " "), string(b)
}

// argvInner is the common tail handler: given the args after the subcommand, it
// returns the inner-args JSON. When there are no flags, the bare positional is
// mapped to primaryKey (e.g. "name", "query", "prompt"); when there are flags,
// the flag object wins (and a stray positional is folded into primaryKey only if
// the flags didn't already set it).
func argvInner(tail []string, primaryKey string, arrayKeys, intKeys map[string]bool) string {
	pos, inner := argvToInnerJSON(tail, arrayKeys, intKeys)
	if inner == "{}" {
		if pos == "" {
			return "{}"
		}
		b, _ := json.Marshal(map[string]string{primaryKey: pos})
		return string(b)
	}
	if pos != "" && primaryKey != "" {
		var m map[string]interface{}
		if json.Unmarshal([]byte(inner), &m) == nil {
			if _, ok := m[primaryKey]; !ok {
				m[primaryKey] = pos
				if b, err := json.Marshal(m); err == nil {
					return string(b)
				}
			}
		}
	}
	return inner
}
