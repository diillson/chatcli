/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"testing"

	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

// A compaction holds the turn for as long as its own LLM call takes, which
// from the terminal reads as a hang. The bracket every compaction already
// goes through makes it a span that ends with the outcome the hooks get.
func TestCompactionIsReportedWithItsOutcome(t *testing.T) {
	collect := pulseWatch(t, pulse.KindBackground)
	c := &ChatCLI{logger: zap.NewNop()} // no hook manager: the taps must not depend on one

	c.beforeCompaction(context.Background(), compactTriggerAuto)
	c.noteCompactionApplied(context.Background(), compactTriggerAuto)
	c.beforeCompaction(context.Background(), compactTriggerRecovery)
	c.compactionSkipped(context.Background(), compactTriggerRecovery)
	c.beforeCompaction(context.Background(), compactTriggerAuto) // never reports an outcome...
	c.beforeCompaction(context.Background(), compactTriggerAuto) // ...and the next one closes it as cancelled
	c.pulseCompactionEnd(compactOutcomeApplied)
	c.pulseCompactionEnd(compactOutcomeApplied) // nothing in flight: no-op

	evs := collect(8)
	type row struct {
		phase  pulse.Phase
		status string
		state  string
	}
	var ends []row
	for _, ev := range evs {
		assert.Equal(t, pulseCompactionNode, ev.Name)
		if ev.Phase == pulse.PhaseEnd {
			ends = append(ends, row{ev.Phase, ev.Status, ev.Attrs["state"]})
		}
	}
	assert.Equal(t, []row{
		{pulse.PhaseEnd, pulse.StatusOK, compactOutcomeApplied},
		{pulse.PhaseEnd, pulse.StatusOK, compactOutcomeSkipped},
		{pulse.PhaseEnd, pulse.StatusCancelled, ""},
		{pulse.PhaseEnd, pulse.StatusOK, compactOutcomeApplied},
	}, ends)
	assert.Equal(t, compactTriggerAuto, evs[1].Attrs["trigger"])

	var nilCLI *ChatCLI
	nilCLI.pulseCompactionEnd("x")
}

func TestMemoryWorkerPointFollowsTheBus(t *testing.T) {
	before := pulse.Default().Stats().Published
	pulseMemoryWorkerPoint("off")
	assert.Equal(t, before, pulse.Default().Stats().Published)

	collect := pulseWatch(t, pulse.KindBackground)
	pulseMemoryWorkerPoint("wrote 2 rollup digests")
	ev := collect(1)[0]
	assert.Equal(t, pulseMemoryWorkerNode, ev.Name)
	assert.Equal(t, "wrote 2 rollup digests", ev.Attrs["state"])
}
