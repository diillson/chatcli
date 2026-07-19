/*
 * ChatCLI - Compaction tool-block boundary snapping tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"fmt"
	"testing"

	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func toolBlock(id, out string) []models.Message {
	return []models.Message{
		{Role: "assistant", ToolCalls: []models.ToolCall{{ID: id, Name: "web_fetch", Type: "function"}}},
		models.NewToolResultMessage(id, out, false, ""),
	}
}

func TestSnapToToolBlockBoundary_MovesCutToOwningAssistant(t *testing.T) {
	history := []models.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "go"},
		{Role: "assistant", ToolCalls: []models.ToolCall{
			{ID: "web_fetch:0", Name: "web_fetch"}, {ID: "web_fetch:1", Name: "web_fetch"},
		}},
		models.NewToolResultMessage("web_fetch:0", "a", false, ""),
		models.NewToolResultMessage("web_fetch:1", "b", false, ""),
		{Role: "assistant", Content: "done"},
	}

	// A cut landing on either tool result must snap back to the assistant.
	assert.Equal(t, 2, snapToToolBlockBoundary(history, 3, 1))
	assert.Equal(t, 2, snapToToolBlockBoundary(history, 4, 1))
	// Cuts on non-tool messages are untouched.
	assert.Equal(t, 5, snapToToolBlockBoundary(history, 5, 1))
	assert.Equal(t, 2, snapToToolBlockBoundary(history, 2, 1))
}

func TestEmergencyTruncate_NeverOrphansToolResults(t *testing.T) {
	hc := NewHistoryCompactor(zap.NewNop())
	cfg := DefaultCompactConfig("MOONSHOT", "kimi-k3")
	cfg.MinKeepRecent = 3

	history := []models.Message{{Role: "system", Content: "sys"}}
	for i := 0; i < 6; i++ {
		history = append(history, models.Message{Role: "user", Content: fmt.Sprintf("u%d", i)})
		history = append(history, toolBlock(fmt.Sprintf("web_fetch:%d", i), "out")...)
	}
	// MinKeepRecent=3 lands the cut mid-block for this shape.
	out := hc.emergencyTruncate(history, cfg)

	// The kept tail must never START with an orphan tool result, and the
	// whole result must be a provider-valid pairing shape.
	require.NotEmpty(t, out)
	assert.True(t, agent.ValidateToolResultPairing(out),
		"emergencyTruncate produced a history the pairing validator rejects")
}
