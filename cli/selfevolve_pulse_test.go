/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A skill the engine authors or evolves shows on the live dashboard: the
// pass as a background span with its outcome, and the skill as a point on
// its own node. Before this it happened in silence.
func TestSkillEvolutionReportsToTheDashboard(t *testing.T) {
	dir := t.TempDir()
	plugins.SetSkillsDirOverride(dir)
	t.Cleanup(func() { plugins.SetSkillsDirOverride("") })
	cliObj := newTestCLI()

	bus := pulse.Default()
	ch, cancel := bus.Subscribe(256)
	bus.SetEnabled(true)
	t.Cleanup(func() { bus.SetEnabled(false); cancel() })

	sum := cliObj.applySkillCandidates(context.Background(), candidateResponse, selfEvolveAuto, stubMerger)
	require.Equal(t, []string{"deploy-x"}, sum.Authored)

	var spanStart, spanEnd, skillPoint *pulse.Event
	deadline := time.After(3 * time.Second)
	for spanEnd == nil || skillPoint == nil {
		select {
		case got := <-ch:
			ev := got
			switch {
			case ev.Kind == pulse.KindBackground && ev.Name == pulseSkillEvolutionNode && ev.Phase == pulse.PhaseStart:
				spanStart = &ev
			case ev.Kind == pulse.KindBackground && ev.Name == pulseSkillEvolutionNode && ev.Phase == pulse.PhaseEnd:
				spanEnd = &ev
			case ev.Kind == pulse.KindSkill && ev.Name == "deploy-x" && ev.Phase == pulse.PhasePoint:
				skillPoint = &ev
			}
		case <-deadline:
			t.Fatalf("missing events: start=%v end=%v skill=%v", spanStart != nil, spanEnd != nil, skillPoint != nil)
		}
	}
	require.NotNil(t, spanStart, "the pass opens a node while it runs")
	// Span attributes travel on the end event.
	assert.Equal(t, "auto", spanEnd.Attrs["mode"])
	assert.Equal(t, "1", spanEnd.Attrs["candidates"])
	assert.Equal(t, pulse.StatusOK, spanEnd.Status)
	assert.Equal(t, "1 authored", spanEnd.Attrs["state"])
	assert.Equal(t, "created", skillPoint.Attrs["state"])

	// Nothing to do, nothing on the dashboard: an extraction with no
	// candidates must not light the node every few minutes.
	time.Sleep(50 * time.Millisecond) // let the pump drain what was published above
	before := bus.Stats().Published
	cliObj.applySkillCandidates(context.Background(), "no candidates here", selfEvolveAuto, stubMerger)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, before, bus.Stats().Published)
}

func TestSelfEvolveSummaryOutcome(t *testing.T) {
	assert.Equal(t, "no change", selfEvolveSummary{}.outcome())
	assert.Equal(t, "1 authored · 2 evolved · 1 failed", selfEvolveSummary{Authored: []string{"a"}, Evolved: []string{"b"}, EvolvedBackup: []string{"c"}, Failed: 1}.outcome())
	assert.Equal(t, "1 redirected · 2 suggested", selfEvolveSummary{Redirected: []string{"x → y"}, Suggested: []string{"p", "q"}}.outcome())
}
