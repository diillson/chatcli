/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package toolshim

import (
	"testing"

	"github.com/diillson/chatcli/models"
	"github.com/stretchr/testify/assert"
)

// TestMarkOpenAICompatibleToolError_PassesThroughWhenNotError is the
// happy path: a non-error tool message goes through unchanged, so the
// marker doesn't pollute successful results.
func TestMarkOpenAICompatibleToolError_PassesThroughWhenNotError(t *testing.T) {
	msg := models.Message{
		Role:       "tool",
		Content:    "Operation completed",
		ToolCallID: "call_1",
	}
	assert.Equal(t, "Operation completed", MarkOpenAICompatibleToolError(msg))
}

// TestMarkOpenAICompatibleToolError_PrependsCodeWhenAvailable confirms
// the [ERROR:<code>] marker is added when both IsError and ErrorCode
// are present. The code carries the stable classification (ENOENT,
// Timeout, etc.) the model can pattern-match on.
func TestMarkOpenAICompatibleToolError_PrependsCodeWhenAvailable(t *testing.T) {
	msg := models.Message{
		Role:       "tool",
		Content:    "could not open file",
		ToolCallID: "call_1",
		IsError:    true,
		ErrorCode:  "ENOENT",
	}
	got := MarkOpenAICompatibleToolError(msg)
	assert.Equal(t, "[ERROR:ENOENT] could not open file", got)
}

// TestMarkOpenAICompatibleToolError_FallsBackToBareMarker covers the
// case where IsError is set but no specific code was classified — we
// still want a clear signal, so we use a bare [ERROR] prefix.
func TestMarkOpenAICompatibleToolError_FallsBackToBareMarker(t *testing.T) {
	msg := models.Message{
		Role:       "tool",
		Content:    "something went wrong",
		ToolCallID: "call_2",
		IsError:    true,
		// ErrorCode intentionally empty
	}
	got := MarkOpenAICompatibleToolError(msg)
	assert.Equal(t, "[ERROR] something went wrong", got)
}

// TestMarkOpenAICompatibleToolError_EmptyContent keeps the marker
// alone when there's no body — still better than a blank tool message
// that the model has to guess at.
func TestMarkOpenAICompatibleToolError_EmptyContent(t *testing.T) {
	msg := models.Message{
		Role:       "tool",
		Content:    "",
		ToolCallID: "call_3",
		IsError:    true,
		ErrorCode:  "Timeout",
	}
	assert.Equal(t, "[ERROR:Timeout] ", MarkOpenAICompatibleToolError(msg))
}

// TestMarkOpenAICompatibleToolError_PreservesNonErrorWithCode is a
// guard: if a caller accidentally sets ErrorCode without IsError, the
// content stays clean. The marker is gated on IsError, not on
// ErrorCode presence, so misconfigured callers don't pollute output.
func TestMarkOpenAICompatibleToolError_PreservesNonErrorWithCode(t *testing.T) {
	msg := models.Message{
		Role:       "tool",
		Content:    "still ok",
		ToolCallID: "call_4",
		IsError:    false,
		ErrorCode:  "ENOENT", // bogus pairing
	}
	assert.Equal(t, "still ok", MarkOpenAICompatibleToolError(msg))
}
