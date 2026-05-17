/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/diillson/chatcli/models"
	"github.com/stretchr/testify/assert"
)

// TestToolContext_RecordFilesDedupes pins the contract: tools may
// report the same path multiple times across iterations, and the
// orchestrator's read/write tracker must collapse duplicates so the
// downstream allowlist doesn't grow without bound.
func TestToolContext_RecordFilesDedupes(t *testing.T) {
	ctx := &ToolContext{}
	ctx.RecordFileRead("/tmp/a")
	ctx.RecordFileRead("/tmp/a")
	ctx.RecordFileRead("/tmp/b")
	assert.Equal(t, []string{"/tmp/a", "/tmp/b"}, ctx.FilesRead)

	ctx.RecordFileWrite("/var/log/x")
	ctx.RecordFileWrite("/var/log/x")
	assert.Equal(t, []string{"/var/log/x"}, ctx.FilesWritten)
}

// TestToolContext_Extra confirms the free-form bag works for both
// "store and retrieve" and "missing key" lookups, and lazy-inits the map.
func TestToolContext_Extra(t *testing.T) {
	ctx := &ToolContext{}
	v, ok := ctx.GetExtra("never-set")
	assert.False(t, ok)
	assert.Nil(t, v)

	ctx.PutExtra("answer", 42)
	v, ok = ctx.GetExtra("answer")
	assert.True(t, ok)
	assert.Equal(t, 42, v)
}

// TestToolContext_NilReceiverSafe documents that the helpers tolerate a
// nil receiver — handy in unit tests and in the bootstrap path before
// the orchestrator has allocated a context.
func TestToolContext_NilReceiverSafe(t *testing.T) {
	var ctx *ToolContext
	assert.NotPanics(t, func() {
		ctx.RecordFileRead("/tmp/x")
		ctx.RecordFileWrite("/tmp/x")
		ctx.RecordHostContacted("example.com")
		ctx.PutExtra("k", 1)
		_, _ = ctx.GetExtra("k")
		ctx.ApplyMutations(nil)
		ctx.Merge(nil)
	})
}

// TestToolContext_ApplyMutationsRunsSerially is the heart of the
// ContextMutation contract: even though concurrent tools may produce
// results in parallel, the mutations are applied in the result-slice's
// order, so writes are observable in a well-defined sequence.
func TestToolContext_ApplyMutationsRunsSerially(t *testing.T) {
	ctx := &ToolContext{}
	results := []ToolResult{
		{ContextMutation: func(c *ToolContext) { c.RecordFileRead("a") }},
		{ContextMutation: func(c *ToolContext) { c.RecordFileRead("b") }},
		{ContextMutation: func(c *ToolContext) { c.RecordFileRead("c") }},
		{}, // no mutation — must not panic / skip silently
	}
	ctx.ApplyMutations(results)
	assert.Equal(t, []string{"a", "b", "c"}, ctx.FilesRead)
}

// TestToolContext_MergeDedupesAcrossContexts ensures the per-turn
// context can be folded into the session context without re-dedup
// boilerplate at the call site.
func TestToolContext_MergeDedupesAcrossContexts(t *testing.T) {
	session := &ToolContext{}
	session.RecordFileRead("/etc/hosts")

	turn := &ToolContext{}
	turn.RecordFileRead("/etc/hosts")
	turn.RecordFileRead("/etc/resolv.conf")
	turn.RecordFileWrite("/tmp/out")
	turn.RecordHostContacted("api.example.com")
	turn.PutExtra("model", "claude-opus")

	session.Merge(turn)

	assert.Equal(t, []string{"/etc/hosts", "/etc/resolv.conf"}, session.FilesRead)
	assert.Equal(t, []string{"/tmp/out"}, session.FilesWritten)
	assert.Equal(t, []string{"api.example.com"}, session.HostsContacted)
	v, ok := session.GetExtra("model")
	assert.True(t, ok)
	assert.Equal(t, "claude-opus", v)
}

// TestWrapLegacyOutput_Success builds a result for the happy path:
// non-empty output, no error, no IsError, no code.
func TestWrapLegacyOutput_Success(t *testing.T) {
	res := WrapLegacyOutput("hello world", nil)
	assert.Equal(t, "hello world", res.Output)
	assert.False(t, res.IsError)
	assert.Empty(t, res.ErrorCode)
}

// TestWrapLegacyOutput_ErrorAppended ensures the error string is
// appended to the output (so the model sees it) and that the code
// classification is populated.
func TestWrapLegacyOutput_ErrorAppended(t *testing.T) {
	err := &fs.PathError{Op: "open", Path: "/nx", Err: fs.ErrNotExist}
	res := WrapLegacyOutput("partial output\n", err)
	assert.True(t, res.IsError)
	assert.Equal(t, "ENOENT", res.ErrorCode)
	assert.Contains(t, res.Output, "partial output")
	assert.Contains(t, res.Output, "error:")
}

// TestWrapLegacyOutput_ErrorReplacesEmptyOutput keeps the model from
// seeing a blank tool_result when only an error was reported.
func TestWrapLegacyOutput_ErrorReplacesEmptyOutput(t *testing.T) {
	res := WrapLegacyOutput("", errors.New("boom"))
	assert.True(t, res.IsError)
	assert.Equal(t, "boom", res.Output)
}

// fakeCarrier is a minimal structuredCarrier for the wrapper tests so
// we don't need the cli/plugins package here (and avoid the import
// cycle that's the whole reason structuredCarrier exists).
type fakeCarrier struct {
	output    string
	isError   bool
	errorCode string
	meta      map[string]any
}

func (f fakeCarrier) GetOutput() string         { return f.output }
func (f fakeCarrier) GetIsError() bool          { return f.isError }
func (f fakeCarrier) GetErrorCode() string      { return f.errorCode }
func (f fakeCarrier) GetMCPMeta() map[string]any { return f.meta }

// TestWrapStructuredResult_PassesThroughFields confirms a plugin-supplied
// structured result lands in the ToolResult unchanged when there's no
// infra error to override the diagnostic.
func TestWrapStructuredResult_PassesThroughFields(t *testing.T) {
	sr := fakeCarrier{
		output:    "ok",
		isError:   false,
		errorCode: "",
		meta:      map[string]any{"hint": "use grep next time"},
	}
	res := WrapStructuredResult(sr, nil)
	assert.Equal(t, "ok", res.Output)
	assert.False(t, res.IsError)
	assert.Equal(t, "use grep next time", res.MCPMeta["hint"])
}

// TestWrapStructuredResult_InfraErrorOverrides documents that an
// infrastructure-level failure (timeout, ctx cancel, plugin crash) is
// surfaced as IsError=true even when the plugin set IsError=false in
// the result. This protects against plugins that swallow errors.
func TestWrapStructuredResult_InfraErrorOverrides(t *testing.T) {
	sr := fakeCarrier{output: "partial", isError: false}
	res := WrapStructuredResult(sr, context.DeadlineExceeded)
	assert.True(t, res.IsError)
	assert.Equal(t, "Timeout", res.ErrorCode)
	assert.Contains(t, res.Output, "partial")
}

// TestWrapStructuredResult_KeepsPluginErrorCode ensures we don't
// overwrite a deliberate plugin-side classification when wrapping.
func TestWrapStructuredResult_KeepsPluginErrorCode(t *testing.T) {
	sr := fakeCarrier{output: "no", isError: true, errorCode: "BusinessError:ItemNotFound"}
	res := WrapStructuredResult(sr, nil)
	assert.True(t, res.IsError)
	assert.Equal(t, "BusinessError:ItemNotFound", res.ErrorCode)
}

// TestClassifyErrorCode_OSPathError covers the most common case:
// stdlib file ops wrap syscall errors in *fs.PathError.
func TestClassifyErrorCode_OSPathError(t *testing.T) {
	_, err := os.Open("/nonexistent/path/that/does/not/exist")
	assert.Equal(t, "ENOENT", ClassifyErrorCode(err))
}

// TestClassifyErrorCode_ContextCanceled / Timeout / nil
func TestClassifyErrorCode_ContextErrors(t *testing.T) {
	assert.Equal(t, "Canceled", ClassifyErrorCode(context.Canceled))
	assert.Equal(t, "Timeout", ClassifyErrorCode(context.DeadlineExceeded))
	assert.Empty(t, ClassifyErrorCode(nil))
}

// TestClassifyErrorCode_ExitError verifies the ExitCode:<N> shape.
func TestClassifyErrorCode_ExitError(t *testing.T) {
	if _, err := exec.LookPath("false"); err != nil {
		t.Skip("'false' not available on PATH")
	}
	cmd := exec.Command("false")
	err := cmd.Run()
	got := ClassifyErrorCode(err)
	assert.Contains(t, got, "ExitCode:")
}

// TestClassifyErrorCode_FallbackUsesLeadingWord catches the case where
// no syscall / path / context match applies — we still want a stable
// code derived from the message rather than UnknownError everywhere.
func TestClassifyErrorCode_FallbackUsesLeadingWord(t *testing.T) {
	err := fmt.Errorf("json: cannot unmarshal value")
	got := ClassifyErrorCode(err)
	assert.Equal(t, "JsonError", got)
}

// TestClassifyErrorCode_PureUnknown ensures the very last fallback is
// the literal UnknownError sentinel — emptyish errors don't crash.
func TestClassifyErrorCode_PureUnknown(t *testing.T) {
	err := errors.New("")
	got := ClassifyErrorCode(err)
	assert.Equal(t, "UnknownError", got)
}

// TestTelemetrySafe_TrimsAndStripsNewlines keeps the log field on one line.
func TestTelemetrySafe_TrimsAndStripsNewlines(t *testing.T) {
	err := errors.New("line one\nline two\n   trailing")
	got := TelemetrySafe(err)
	assert.NotContains(t, got, "\n")
}

// TestToolResult_DurationSet documents that callers populate Duration
// directly on the struct (no setter) — we pin that the field is plain
// time.Duration and round-trips correctly.
func TestToolResult_DurationSet(t *testing.T) {
	res := ToolResult{Output: "x", Duration: 250 * time.Millisecond}
	assert.Equal(t, 250*time.Millisecond, res.Duration)
}

// TestToolResult_NewMessagesAppended is a smoke test that the slice is
// usable directly by the orchestrator (no constructor / no special API).
func TestToolResult_NewMessagesAppended(t *testing.T) {
	res := ToolResult{
		Output: "done",
		NewMessages: []models.Message{
			{Role: "system", Content: "output truncated"},
		},
	}
	assert.Len(t, res.NewMessages, 1)
	assert.Equal(t, "system", res.NewMessages[0].Role)
}
