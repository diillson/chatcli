/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Provider context edits, mirrored locally.
 *
 * With the provider context engine on (Anthropic context editing), the
 * server clears the oldest tool results from the request it processes.
 * Until now that stayed invisible here: the local history kept shipping
 * the cleared results, the footer and the compactor over-estimated, the
 * calibrator saw chars ≫ tokens and the next request's cache write
 * counted as a miss. mirrorContextEdits closes the loop.
 */
package cli

import (
	"strings"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// contextEditKeepToolUses mirrors the keep threshold of the request block
// (client.AnthropicContextManagement: the five most recent tool uses).
const contextEditKeepToolUses = 5

// clearedToolResultStub replaces a tool result the provider already
// dropped; the original stays recoverable through CCR when archived.
const clearedToolResultStub = "[tool result cleared by the provider context engine]"

// mirrorContextEdits stubs the oldest tool results the provider cleared
// (keeping the most recent tool uses, like the server), archives the
// originals to CCR, notes the expected cache rebuild and books the
// cleared tokens. Returns how many results were stubbed.
func (cli *ChatCLI) mirrorContextEdits(edits *models.ContextEdits) int {
	if cli == nil || edits == nil || edits.ClearedToolUses <= 0 {
		return 0
	}
	// Tool-use groups oldest→newest: an assistant message with tool calls
	// and the tool results answering it.
	type group struct{ results []int }
	var groups []group
	for i, m := range cli.history {
		switch {
		case strings.EqualFold(m.Role, "assistant") && len(m.ToolCalls) > 0:
			groups = append(groups, group{})
		case strings.EqualFold(m.Role, "tool") && len(groups) > 0:
			g := &groups[len(groups)-1]
			g.results = append(g.results, i)
		}
	}
	clearable := len(groups) - contextEditKeepToolUses
	if clearable > edits.ClearedToolUses {
		clearable = edits.ClearedToolUses
	}
	stubbed := 0
	for gi := 0; gi < clearable && gi < len(groups); gi++ {
		for _, idx := range groups[gi].results {
			m := &cli.history[idx]
			if m.Content == "" || strings.HasPrefix(m.Content, clearedToolResultStub) {
				continue
			}
			stub := clearedToolResultStub
			if cli.compressionLayer != nil {
				if key, ok := cli.compressionLayer.Archive(m.Content); ok && key != "" {
					stub += " — recover with @recall " + key
				}
			}
			m.Content = stub
			if m.Meta != nil {
				m.Meta.PreserveVerbatim = false
			}
			stubbed++
		}
	}
	if cli.costTracker != nil {
		cli.costTracker.RecordContextEdits(edits.ClearedToolUses, edits.ClearedInputTokens)
		if stubbed > 0 {
			cli.costTracker.NoteExpectedCacheRebuild()
		}
	}
	if cli.logger != nil {
		cli.logger.Info("provider context edits mirrored locally",
			zap.Int("cleared_tool_uses", edits.ClearedToolUses), zap.Int("cleared_input_tokens", edits.ClearedInputTokens), zap.Int("stubbed", stubbed))
	}
	return stubbed
}
