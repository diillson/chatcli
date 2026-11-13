/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * @context verbs and options that bring the tool level with /context,
 * behind an optional adapter interface so an adapter written against the
 * original ContextAdapter keeps compiling.
 */
package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// ContextAttachOptions carries every option /context attach accepts.
type ContextAttachOptions struct {
	RagTopK  int     // > 0: semantic top-K retrieval; 0 leaves the default
	Priority int     // order among attachments (lower first); 0 = default
	Chunks   []int   // 1-based chunks of a chunked context; empty = all
	Weight   float64 // corpus weight when knowledge hits merge; 0 = neutral
	Full     bool    // whole-content injection, no automatic retrieval
}

// ContextMaintainer is the optional part of the adapter behind the verbs the
// tool gained for parity with /context.
type ContextMaintainer interface {
	// CreateTagged is Create plus tags.
	CreateTagged(name, mode string, paths []string, description string, tags []string, force bool) (string, error)
	// AttachWith is Attach with the full option set.
	AttachWith(name string, opts ContextAttachOptions) (string, error)
	// Refresh re-indexes a context from its recorded source paths.
	Refresh(ctx context.Context, name string) (string, error)
	// Watch re-indexes a context whenever its sources change; an empty name
	// lists the contexts being watched.
	Watch(name string) (string, error)
	// Unwatch stops watching a context.
	Unwatch(name string) (string, error)
}

// contextKnownArgs lists, per verb, every argument Execute reads. A verb
// listed here rejects anything else: a misspelled or misplaced argument
// (attach with "mode":"full") used to be dropped and the call ran with
// defaults the model never asked for.
var contextKnownArgs = map[string][]string{
	"attach":  {"name", "rag", "retrieve", "priority", "chunks", "chunk", "weight", "full"},
	"refresh": {"name"},
	"watch":   {"name"},
	"unwatch": {"name"},
}

// contextCheckArgs reports the first argument cmd does not read.
func contextCheckArgs(cmd string, raw map[string]json.RawMessage) error {
	known, ok := contextKnownArgs[cmd]
	if !ok {
		return nil
	}
	for k := range raw {
		// The flat envelope (no "args" object) carries the verb itself.
		if k == "cmd" || k == "command" || k == "action" {
			continue
		}
		if !slices.Contains(known, k) {
			return fmt.Errorf("@context %s: unknown argument %q (valid: %s)", cmd, k, strings.Join(known, ", "))
		}
	}
	return nil
}

// contextCreate builds a context, with tags when the adapter supports them.
func contextCreate(adapter ContextAdapter, raw map[string]json.RawMessage) (string, error) {
	name := strings.TrimSpace(jsonString(raw, "name"))
	if name == "" {
		return "", fmt.Errorf("@context create: \"name\" is required")
	}
	paths := contextStringSlice(raw, "paths", "path", "source", "corpus")
	if len(paths) == 0 {
		return "", fmt.Errorf("@context create: at least one source path is required (a corpus.jsonl, directory or file)")
	}
	mode := strings.TrimSpace(jsonString(raw, "mode"))
	if mode == "" {
		mode = "knowledge"
	}
	desc := jsonString(raw, "description", "desc")
	tags := contextStringSlice(raw, "tags", "tag")
	if len(tags) == 0 {
		out, err := adapter.Create(name, mode, paths, desc, jsonBool(raw, "force"))
		return wrapContextErr("create", out, err)
	}
	m, ok := adapter.(ContextMaintainer)
	if !ok {
		return "", fmt.Errorf("@context create: tags are not available in this session")
	}
	out, err := m.CreateTagged(name, mode, paths, desc, tags, jsonBool(raw, "force"))
	return wrapContextErr("create", out, err)
}

// contextAttach attaches a context with the same options /context attach has.
func contextAttach(adapter ContextAdapter, raw map[string]json.RawMessage) (string, error) {
	name := strings.TrimSpace(jsonString(raw, "name"))
	if name == "" {
		return "", fmt.Errorf("@context attach: \"name\" is required")
	}
	opts, err := contextAttachOptions(raw)
	if err != nil {
		return "", fmt.Errorf("@context attach: %w", err)
	}
	m, ok := adapter.(ContextMaintainer)
	if !ok {
		if len(opts.Chunks) > 0 || opts.Weight != 0 || opts.Full {
			return "", fmt.Errorf("@context attach: chunks/weight/full are not available in this session")
		}
		out, aerr := adapter.Attach(name, opts.RagTopK, opts.Priority)
		return wrapContextErr("attach", out, aerr)
	}
	out, aerr := m.AttachWith(name, opts)
	return wrapContextErr("attach", out, aerr)
}

// contextAttachOptions reads and type-checks the attach options. A value
// that does not parse is an error: falling back to the default would attach
// the context in a shape the model did not request.
func contextAttachOptions(raw map[string]json.RawMessage) (ContextAttachOptions, error) {
	var opts ContextAttachOptions
	var err error
	if opts.RagTopK, err = contextRagArg(raw); err != nil {
		return opts, err
	}
	if v, ok := raw["priority"]; ok {
		n, perr := strconv.Atoi(contextScalarText(v))
		if perr != nil {
			return opts, fmt.Errorf("\"priority\" must be an integer, got %s", string(v))
		}
		opts.Priority = n
	}
	if v, ok := raw["weight"]; ok {
		w, perr := strconv.ParseFloat(contextScalarText(v), 64)
		// Zero is rejected here, where "0" and "not given" still differ.
		if perr != nil || w <= 0 || w > 10 {
			return opts, fmt.Errorf("\"weight\" must be a number above 0 and up to 10, got %s", string(v))
		}
		opts.Weight = w
	}
	if opts.Chunks, err = contextIntList(raw, "chunks", "chunk"); err != nil {
		return opts, err
	}
	opts.Full = jsonBool(raw, "full")
	return opts, nil
}

// contextRagArg reads rag: true (default top-K), a positive integer K, or
// absent/false (no override). Anything else is reported.
func contextRagArg(raw map[string]json.RawMessage) (int, error) {
	v, ok := raw["rag"]
	if !ok {
		v, ok = raw["retrieve"]
	}
	if !ok || len(v) == 0 {
		return 0, nil
	}
	var b bool
	if json.Unmarshal(v, &b) == nil {
		if b {
			return contextDefaultRagTopK, nil
		}
		return 0, nil
	}
	n, err := strconv.Atoi(contextScalarText(v))
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("\"rag\" must be true or a positive integer, got %s (to inject the whole content pass \"full\":true)", string(v))
	}
	return n, nil
}

// contextScalarText renders a JSON number or string as trimmed text, so the
// same parser serves the JSON envelope (8) and the flattened argv ("8").
func contextScalarText(v json.RawMessage) string {
	var s string
	if json.Unmarshal(v, &s) == nil {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(string(v))
}

// contextIntList reads a list of integers given as a JSON array ([1,2]), as
// repeated flags (["1","2"]) or as one comma-separated string ("1,2").
func contextIntList(raw map[string]json.RawMessage, keys ...string) ([]int, error) {
	for _, k := range keys {
		v, ok := raw[k]
		if !ok || len(v) == 0 {
			continue
		}
		var items []json.RawMessage
		if json.Unmarshal(v, &items) != nil {
			items = []json.RawMessage{v}
		}
		var out []int
		for _, item := range items {
			for _, part := range strings.Split(contextScalarText(item), ",") {
				n, err := strconv.Atoi(strings.TrimSpace(part))
				if err != nil {
					return nil, fmt.Errorf("%q must be chunk numbers (e.g. [1,3]), got %s", k, string(v))
				}
				out = append(out, n)
			}
		}
		return out, nil
	}
	return nil, nil
}

// contextMaintain runs refresh / watch / unwatch.
func contextMaintain(ctx context.Context, adapter ContextAdapter, cmd string, raw map[string]json.RawMessage) (string, error) {
	m, ok := adapter.(ContextMaintainer)
	if !ok {
		return "", fmt.Errorf("@context %s: not available in this session", cmd)
	}
	name := strings.TrimSpace(jsonString(raw, "name"))
	if name == "" && cmd != "watch" {
		return "", fmt.Errorf("@context %s: \"name\" is required", cmd)
	}
	var out string
	var err error
	switch cmd {
	case "refresh":
		out, err = m.Refresh(ctx, name)
	case "watch":
		out, err = m.Watch(name)
	default:
		out, err = m.Unwatch(name)
	}
	return wrapContextErr(cmd, out, err)
}
