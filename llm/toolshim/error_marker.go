/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package toolshim

import "github.com/diillson/chatcli/models"

// MarkOpenAICompatibleToolError returns the content to put in a
// {"role":"tool","tool_call_id":...,"content":<here>} envelope, with a
// stable [ERROR:<code>] prefix when the agent layer flagged the result
// as an error and there is no native is_error wire field (OpenAI Chat
// Completions, Moonshot, MiniMax, ZAI, OpenRouter, xAI, Copilot, …).
//
// The marker is a fixed pattern the model recognizes: it gives the
// LLM an at-a-glance signal for "this tool returned an error, consider
// retry or a different approach" without dropping the actual output.
//
// Providers with native error support (Anthropic via is_error,
// Google AI via function_response.response.error) are handled in their
// own provider adapters and should not call this helper.
func MarkOpenAICompatibleToolError(msg models.Message) string {
	if !msg.IsError {
		return msg.Content
	}
	if msg.ErrorCode != "" {
		return "[ERROR:" + msg.ErrorCode + "] " + msg.Content
	}
	return "[ERROR] " + msg.Content
}
