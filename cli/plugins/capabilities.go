/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"context"
	"errors"
)

// errNilPlugin is returned by helpers that get a nil plugin pointer.
// Exposed as a package error rather than a string so callers can match
// it with errors.Is when validating registry lookups.
var errNilPlugin = errors.New("plugins: nil plugin")

// This file defines optional capability interfaces that plugins MAY
// implement to opt into richer orchestration features. Every assertion is
// fail-closed: if a plugin does not implement an interface, the system
// assumes the most restrictive answer (not concurrency-safe, not
// read-only, no contextual description). External plugins built against
// the legacy Plugin contract therefore continue to work unchanged —
// they just don't benefit from parallelization or per-call UI.

// ReadOnlyAware is implemented by plugins that can report whether a
// specific invocation has side effects. Read-only plugins skip the
// security confirmation prompt by default (modulo policy rules) and
// participate in the concurrent batch alongside other read-only tools.
//
// The decision is per-input — `Read("/etc/passwd")` is read-only;
// `Read("--mutate-cache")` (hypothetical) would not be. The validator
// for the actual side-effect lives inside the plugin, where the schema
// is known.
type ReadOnlyAware interface {
	IsReadOnly(args []string) bool
}

// ConcurrencySafeAware is implemented by plugins whose invocations can
// safely run in parallel with other concurrency-safe invocations of the
// same OR different plugins. A plugin that mutates global state (writes
// files, edits a database, mutates the agent's tool context) MUST return
// false to opt out.
//
// The orchestrator partitions a turn's tool calls into batches where
// each batch is either all-concurrency-safe (run in parallel up to
// CHATCLI_MAX_TOOL_CONCURRENCY) or all-serial. Mixed batches are split
// to preserve the relative order of serial steps.
type ConcurrencySafeAware interface {
	IsConcurrencySafe(args []string) bool
}

// DescriberWithInput supplements the static Description() with a
// contextual one-liner for the spinner / progress UI. Example:
//
//	Read("/etc/hosts") -> "Reading /etc/hosts"
//	Search("Login")    -> "Searching for 'Login'"
//
// The returned text MUST already be i18n-resolved by the plugin (it
// owns the locale lookup). Callers display it verbatim.
type DescriberWithInput interface {
	DescribeCall(args []string) string
}

// PromptOpts carries the context an LLM-aware plugin needs to produce a
// system-prompt slice. Today only ToolName is used; the struct exists so
// future fields (model family, locale, role mode) can be added without
// breaking the interface signature.
type PromptOpts struct {
	ToolName string
}

// Prompter is implemented by plugins that want to contribute a contextual
// snippet to the system prompt. Useful for tools whose usage instructions
// vary by environment (e.g. a workspace-aware /coder hints differ
// between Go and TypeScript projects).
type Prompter interface {
	Prompt(opts PromptOpts) (string, error)
}

// StreamingInputAware is implemented by plugins that want progressive
// updates as the LLM streams the tool's input arguments. Anthropic
// streams `input_json_delta` token by token; OpenAI streams
// `tool_calls[].function.arguments` deltas. The orchestrator parses
// those into field updates and calls UpdateStreamingInput for plugins
// that opt in.
//
// Typical use: @websearch displays the query as it comes in;
// @webfetch shows the URL the moment it's complete; coder Read shows
// the path. Plugins that don't care leave this unimplemented and only
// see the final args.
type StreamingInputAware interface {
	UpdateStreamingInput(field, value string)
}

// IsReadOnly returns the plugin's read-only status for the given args.
// Returns false (fail-closed) for plugins that don't implement
// ReadOnlyAware. This is the single point of truth for the orchestrator;
// never call the interface method directly so the default stays correct.
func IsReadOnly(p Plugin, args []string) bool {
	if p == nil {
		return false
	}
	if r, ok := p.(ReadOnlyAware); ok {
		return r.IsReadOnly(args)
	}
	return false
}

// IsConcurrencySafe returns whether the plugin can run in parallel with
// other concurrency-safe invocations for the given args. Fail-closed.
func IsConcurrencySafe(p Plugin, args []string) bool {
	if p == nil {
		return false
	}
	if c, ok := p.(ConcurrencySafeAware); ok {
		return c.IsConcurrencySafe(args)
	}
	return false
}

// DescribeCall returns the contextual one-liner for the plugin's
// current invocation, or the static Description() as a fallback. The
// caller never needs to do the type assertion itself.
func DescribeCall(p Plugin, args []string) string {
	if p == nil {
		return ""
	}
	if d, ok := p.(DescriberWithInput); ok {
		if s := d.DescribeCall(args); s != "" {
			return s
		}
	}
	return p.Description()
}

// PromptFor extracts a system-prompt slice from a Prompter plugin, if
// any. Returns empty string and no error when the plugin doesn't
// implement Prompter.
func PromptFor(p Plugin, opts PromptOpts) (string, error) {
	if p == nil {
		return "", nil
	}
	if pp, ok := p.(Prompter); ok {
		return pp.Prompt(opts)
	}
	return "", nil
}

// PushStreamingInput delivers a partial-argument update to a plugin that
// implements StreamingInputAware. No-op otherwise.
func PushStreamingInput(p Plugin, field, value string) {
	if p == nil {
		return
	}
	if s, ok := p.(StreamingInputAware); ok {
		s.UpdateStreamingInput(field, value)
	}
}

// StructuredResult is the provider-neutral, agent-internal representation of
// a tool invocation outcome. It mirrors cli/agent.ToolResult but lives in the
// plugins package so plugins (which sit below cli/agent in the dep graph) can
// emit it without an import cycle. The cli/agent layer wraps it into the
// ToolResult type used by the orchestrator.
//
// Fields intentionally mirror ToolResult: Output / IsError / ErrorCode /
// MCPMeta. The richer fields (NewMessages, ContextMutation) live exclusively
// on ToolResult and are filled by the agent layer if it needs them.
type StructuredResult struct {
	Output    string
	IsError   bool
	ErrorCode string
	MCPMeta   map[string]any
}

// GetOutput returns the human/model-readable output. Satisfies the
// agent-side structuredCarrier interface without introducing an import
// of cli/plugins from cli/agent.
func (r StructuredResult) GetOutput() string { return r.Output }

// GetIsError mirrors the IsError field for the structuredCarrier interface.
func (r StructuredResult) GetIsError() bool { return r.IsError }

// GetErrorCode mirrors the ErrorCode field for the structuredCarrier interface.
func (r StructuredResult) GetErrorCode() string { return r.ErrorCode }

// GetMCPMeta mirrors the MCPMeta field for the structuredCarrier interface.
func (r StructuredResult) GetMCPMeta() map[string]any { return r.MCPMeta }

// StructuredExecutor is the optional interface a plugin implements when it
// wants to return a structured result instead of the legacy (string, error)
// pair. The plain Execute / ExecuteWithStream continue to work for plugins
// that have not migrated — the orchestrator wraps their output via
// agent.WrapLegacyOutput.
//
// Streaming callback semantics are identical to ExecuteWithStream.
type StructuredExecutor interface {
	ExecuteStructured(ctx context.Context, args []string, onOutput func(string)) (StructuredResult, error)
}

// RunStructured executes the plugin and always returns a StructuredResult,
// preferring the structured executor when available and falling back to the
// legacy ExecuteWithStream pair otherwise. The infrastructure error (network
// timeout, ctx canceled, plugin binary not found) is returned separately so
// the orchestrator can decide whether to abort the batch.
func RunStructured(ctx context.Context, p Plugin, args []string, onOutput func(string)) (StructuredResult, error) {
	if p == nil {
		return StructuredResult{}, errNilPlugin
	}
	if se, ok := p.(StructuredExecutor); ok {
		return se.ExecuteStructured(ctx, args, onOutput)
	}
	out, err := p.ExecuteWithStream(ctx, args, onOutput)
	res := StructuredResult{Output: out}
	if err != nil {
		res.IsError = true
		// ErrorCode classification lives in cli/agent — callers wrap this
		// StructuredResult with agent.WrapStructuredResult to populate
		// ErrorCode. Keeping the classifier out of this package avoids
		// an import cycle (cli/plugins ↛ cli/agent).
	}
	return res, err
}
