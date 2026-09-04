/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
)

// turnThinkingBlocks resolves the reasoning blocks to attach to the
// assistant turn just produced.
//
// Two sources, in order of confidence: the structured response of a
// tool-aware call already carries them, and the plain send paths leave
// them on the client because their signature returns only a string. The
// blocks are bound to the model that produced them, so a client that
// reports a different model than the one it just answered with — a
// mid-turn route change, a fallback chain — contributes nothing rather
// than a block the next request would be rejected for.
func turnThinkingBlocks(resp *models.LLMResponse, c client.LLMClient) []models.ThinkingBlock {
	if resp != nil && len(resp.Thinking) > 0 {
		return resp.Thinking
	}
	ta, ok := client.AsThinkingAware(c)
	if !ok {
		return nil
	}
	blocks := ta.LastThinking()
	if len(blocks) == 0 {
		return nil
	}
	if m := ta.LastThinkingModel(); m != "" && m != c.GetModelName() {
		return nil
	}
	return blocks
}
