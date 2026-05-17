/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package agent

import (
	"time"

	"github.com/diillson/chatcli/models"
)

// ToolResult is the structured outcome of a tool invocation, decoupled
// from the wire format used to ship results back to the LLM. Each
// provider adapter (claudeai, openai, googleai, …) translates this into
// its own representation in cli/llm/<provider>/tool_result_adapter.go;
// the orchestrator never branches on provider type.
//
// Compared to the legacy `(string, error)` return shape, ToolResult lets
// a plugin:
//
//   - Distinguish business errors from infrastructure errors (IsError
//     vs the returned `error`). Business errors stay inside the
//     conversation as tool_result with is_error=true; infrastructure
//     errors abort the batch.
//   - Tag the error class with a stable code (ENOENT, Timeout, …) so
//     the model can reason about retryability without parsing English.
//   - Emit additional conversational messages alongside the result
//     (e.g. a warning that the output was truncated, a hint that a
//     different tool would be more efficient).
//   - Mutate the orchestrator's per-turn context — record that a file
//     was read, register a new allowed path, push an undo handle. The
//     mutator runs serially after the batch completes so concurrent
//     tools don't race on the same context object.
//   - Pass MCP-style structured metadata through to providers that
//     understand it (Anthropic mcp_meta) without leaking provider
//     specifics into plugin code.
type ToolResult struct {
	// Output is the human/model-readable content. For Anthropic this maps
	// to the tool_result `content` field; for OpenAI it goes into the
	// `tool` message body.
	Output string

	// IsError signals that the tool encountered a business-level failure
	// (the command exited non-zero, the URL returned 4xx, the file was
	// not found). When true, provider adapters set the relevant
	// is_error / [ERROR:<code>] marker so the model knows it's a failure
	// without parsing the body.
	IsError bool

	// ErrorCode is the stable, locale-independent classification. Empty
	// when IsError is false. Examples: "ENOENT", "EACCES", "Timeout",
	// "Canceled", "ExitCode:2", "NetworkError", "UnknownError". Filled
	// by ClassifyError (Fase 5.1) when the plugin returns a Go error;
	// plugins that hand-craft a business error must set this themselves
	// so the dashboard / log fields stay stable.
	ErrorCode string

	// NewMessages lets a tool emit additional conversation entries
	// alongside its result — typically system-role hints
	// ("output truncated to first 5000 chars") or assistant-role
	// auto-suggestions. The orchestrator appends them to history in
	// the order returned; ordering across concurrent tools is undefined
	// (so concurrent tools should not rely on NewMessages for ordering).
	NewMessages []models.Message

	// ContextMutation is an optional callback applied serially by the
	// orchestrator after the entire batch completes. Used to register
	// side effects that need to survive across turns: "I just wrote
	// /tmp/foo, add it to the allowlist", "I just read main.go, mark
	// it as recently touched". Returning nil means "no mutation".
	ContextMutation func(ctx *ToolContext)

	// MCPMeta carries provider-agnostic structured metadata that some
	// adapters can attach to the wire result. Anthropic's tool_result
	// supports a `_meta` field; other providers ignore it. Use sparingly
	// — most use cases are better served by NewMessages or telemetry.
	MCPMeta map[string]any

	// Duration is the wall-clock time the tool took to run, recorded by
	// the orchestrator and exposed here so plugins can include it in
	// telemetry/logs without re-measuring.
	Duration time.Duration
}

// ToolContext is the orchestrator-owned, per-turn state that
// ContextMutation callbacks can edit. It's a small, intentionally
// minimal surface — kept here in the agent package rather than the cli
// package so plugins under cli/plugins/ can manipulate it without an
// import cycle.
//
// Fields populated lazily; nil maps are valid and Mutate helpers handle
// the lazy-init.
type ToolContext struct {
	// FilesRead tracks the absolute paths the tool reported reading.
	// Used by the file-staleness detector to know which paths to watch.
	FilesRead []string

	// FilesWritten tracks paths the tool reported writing. Used by the
	// same staleness detector and by the engine's session workspace
	// allowlist to surface new writes to read-on-demand.
	FilesWritten []string

	// HostsContacted tracks the network hosts the tool contacted.
	// Telemetry / audit use only.
	HostsContacted []string

	// Extra is a free-form bag for forward compatibility. Plugins that
	// need to track something the orchestrator doesn't know about yet
	// can stash it here; consumers must defensively type-assert.
	Extra map[string]any
}

// RecordFileRead appends a path to FilesRead, deduping. Called by tools
// that want to surface a read to the orchestrator's tracking layer
// (file-staleness, undo, audit) without owning a reference to it.
func (c *ToolContext) RecordFileRead(path string) {
	if c == nil || path == "" {
		return
	}
	for _, existing := range c.FilesRead {
		if existing == path {
			return
		}
	}
	c.FilesRead = append(c.FilesRead, path)
}

// RecordFileWrite appends a path to FilesWritten, deduping.
func (c *ToolContext) RecordFileWrite(path string) {
	if c == nil || path == "" {
		return
	}
	for _, existing := range c.FilesWritten {
		if existing == path {
			return
		}
	}
	c.FilesWritten = append(c.FilesWritten, path)
}

// RecordHostContacted appends a host to HostsContacted, deduping.
func (c *ToolContext) RecordHostContacted(host string) {
	if c == nil || host == "" {
		return
	}
	for _, existing := range c.HostsContacted {
		if existing == host {
			return
		}
	}
	c.HostsContacted = append(c.HostsContacted, host)
}

// PutExtra stores a value in the Extra bag, allocating the map on first use.
func (c *ToolContext) PutExtra(key string, value any) {
	if c == nil || key == "" {
		return
	}
	if c.Extra == nil {
		c.Extra = make(map[string]any)
	}
	c.Extra[key] = value
}

// GetExtra returns the value at key, plus an ok flag distinguishing
// "stored as nil" from "missing entirely".
func (c *ToolContext) GetExtra(key string) (any, bool) {
	if c == nil || c.Extra == nil {
		return nil, false
	}
	v, ok := c.Extra[key]
	return v, ok
}

// ApplyMutations runs each callback serially in the order given. The
// orchestrator calls this after a batch of concurrent tools completes —
// the serial application is what makes it safe for tools to mutate the
// shared context from within a parallel batch.
func (c *ToolContext) ApplyMutations(results []ToolResult) {
	if c == nil {
		return
	}
	for _, r := range results {
		if r.ContextMutation != nil {
			r.ContextMutation(c)
		}
	}
}

// Merge copies every recorded entry from other into c, deduping. Used
// by the orchestrator when reconciling per-turn context into the
// session-scoped state.
func (c *ToolContext) Merge(other *ToolContext) {
	if c == nil || other == nil {
		return
	}
	for _, p := range other.FilesRead {
		c.RecordFileRead(p)
	}
	for _, p := range other.FilesWritten {
		c.RecordFileWrite(p)
	}
	for _, h := range other.HostsContacted {
		c.RecordHostContacted(h)
	}
	for k, v := range other.Extra {
		c.PutExtra(k, v)
	}
}

// WrapLegacyOutput builds a ToolResult from the legacy (string, error)
// shape used by every plugin that hasn't migrated to ExecuteStructured.
// The error is mapped through ClassifyErrorCode to fill ErrorCode.
//
// This is the bridge that lets the orchestrator drive every plugin —
// legacy or new — through the same ToolResult-shaped pipeline.
func WrapLegacyOutput(output string, err error) ToolResult {
	res := ToolResult{Output: output}
	if err != nil {
		res.IsError = true
		res.ErrorCode = ClassifyErrorCode(err)
		// Append a one-line error indicator so the model sees the failure
		// even if the legacy output happened to be empty.
		if res.Output == "" {
			res.Output = err.Error()
		} else {
			res.Output = res.Output + "\nerror: " + err.Error()
		}
	}
	return res
}

// WrapStructuredResult elevates a plugins.StructuredResult into the
// agent-level ToolResult shape, filling ErrorCode from the infrastructure
// error when the plugin didn't set it itself. The Duration field is set
// by the orchestrator after this call; callers do not need to fill it.
func WrapStructuredResult(sr structuredCarrier, infraErr error) ToolResult {
	res := ToolResult{
		Output:    sr.GetOutput(),
		IsError:   sr.GetIsError(),
		ErrorCode: sr.GetErrorCode(),
		MCPMeta:   sr.GetMCPMeta(),
	}
	if infraErr != nil {
		res.IsError = true
		if res.ErrorCode == "" {
			res.ErrorCode = ClassifyErrorCode(infraErr)
		}
		if res.Output == "" {
			res.Output = infraErr.Error()
		}
	}
	return res
}

// structuredCarrier is the read-only view of plugins.StructuredResult
// used by WrapStructuredResult. The agent package cannot import
// cli/plugins (would create a cycle), so we define a narrow interface
// that the StructuredResult type satisfies. The legacy carrier methods
// are tiny enough to stay in sync without a generator.
type structuredCarrier interface {
	GetOutput() string
	GetIsError() bool
	GetErrorCode() string
	GetMCPMeta() map[string]any
}
