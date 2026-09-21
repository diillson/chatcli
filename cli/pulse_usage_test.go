/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"testing"

	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every real usage report, whatever the lane, puts on the model node what the
// call consumed and what the model has cost so far, and on the session node
// the running total: the same numbers /cost shows, as they change.
func TestUsageAndCostReachTheModelAndSessionNodes(t *testing.T) {
	ct := NewCostTrackerAt(t.TempDir())
	ct.RecordRealUsageIn(LaneMain, "openai", "gpt-4o", &models.UsageInfo{PromptTokens: 10, CompletionTokens: 5, IsReal: true})
	// Off: nothing above may have been published, and nothing is built.
	require.Nil(t, ct.pulseUsageEventsLocked(LaneMain, &ModelUsageRecord{}, 1, &models.UsageInfo{}))

	collectLLM := pulseWatch(t, pulse.KindLLM)
	ct.RecordRealUsageIn(LaneWorker, "openai", "gpt-4o", &models.UsageInfo{
		PromptTokens: 1200, CompletionTokens: 300, CacheReadInputTokens: 800, TotalTokens: 1500, IsReal: true,
	})

	ev := collectLLM(1)[0]
	assert.Equal(t, pulse.PhaseUpdate, ev.Phase)
	assert.Equal(t, "OPENAI:gpt-4o", ev.Name, "same node name as the request taps, whatever the case the call site used")
	assert.Equal(t, pulseModelNode("OpenAI", "gpt-4o"), ev.Name)
	assert.Equal(t, string(LaneWorker), ev.Attrs["lane"])
	assert.Equal(t, "300", ev.Attrs["last_output_tokens"])
	assert.Equal(t, "800", ev.Attrs["last_cache_read"])
	assert.NotEmpty(t, ev.Attrs["last_input_tokens"])
	assert.Equal(t, "2", ev.Attrs["requests"], "cumulative: the first call happened before the dashboard opened")
	assert.NotEmpty(t, ev.Attrs["tokens"])
	assert.Regexp(t, `^\$\d+\.\d+$`, ev.Attrs["cost"], "a priced model reports what it has cost so far")
	_, hasWrite := ev.Attrs["last_cache_write"]
	assert.False(t, hasWrite, "zero counts are omitted")
}

func TestSessionNodeCarriesTotalCostAndContextWindow(t *testing.T) {
	collect := pulseWatch(t, pulse.KindSession)
	ct := NewCostTrackerAt(t.TempDir())
	ct.RecordRealUsageIn(LaneMain, "openai", "gpt-4o", &models.UsageInfo{PromptTokens: 5000, CompletionTokens: 1000, IsReal: true})
	pulseContextWindow(37, 128000)
	pulseContextWindow(104, 128000) // past 100 is the signal the next turn compacts
	pulseContextWindow(10, 0)       // unknown window: nothing to say

	var usage, ctx []pulse.Event
	for _, ev := range collect(3) {
		if ev.Phase != pulse.PhaseUpdate || ev.ID != pulseSessionNodeID {
			continue // the session snapshot of another test's wiring
		}
		if ev.Attrs["ctx"] != "" {
			ctx = append(ctx, ev)
		} else {
			usage = append(usage, ev)
		}
	}
	require.Len(t, usage, 1)
	assert.Equal(t, "1", usage[0].Attrs["requests"])
	assert.Regexp(t, `^\$`, usage[0].Attrs["cost"])
	require.Len(t, ctx, 2)
	assert.Equal(t, "37%", ctx[0].Attrs["ctx"])
	assert.Equal(t, "128000", ctx[0].Attrs["window"])
	assert.Equal(t, "104%", ctx[1].Attrs["ctx"])
}

func TestFormatPulseUSD(t *testing.T) {
	assert.Equal(t, "", formatPulseUSD(0))
	assert.Equal(t, "", formatPulseUSD(-1))
	assert.Equal(t, "$0.0042", formatPulseUSD(0.0042))
	assert.Equal(t, "$0.01", formatPulseUSD(0.01))
	assert.Equal(t, "$12.35", formatPulseUSD(12.345))
}
