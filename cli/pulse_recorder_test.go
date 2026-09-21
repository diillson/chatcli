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

	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// The scheduler daemon builds no ChatCLI. Without a recorder of its own its
// jobs would be emitted into a bus nobody spools.
func TestStartPulseRecorderGivesAStandaloneProcessASpool(t *testing.T) {
	t.Setenv(pulseDashEnv, "1")
	root, err := pulse.DefaultRoot()
	require.NoError(t, err)
	instance := pulse.Default().Instance()
	_, base, _ := pulse.ReadSince(root, instance, 0, 0)

	stop := StartPulseRecorder(context.Background(), "daemon", zap.NewNop())
	require.Eventually(t, pulse.Default().Enabled, 3*time.Second, 10*time.Millisecond)
	pulse.Emit(pulse.Event{Kind: pulse.KindBackground, Phase: pulse.PhasePoint, ID: "job:x", Name: "scheduler"})

	var session, job bool
	require.Eventually(t, func() bool {
		evs, _, _ := pulse.ReadSince(root, instance, base, 0)
		for _, ev := range evs {
			session = session || (ev.Kind == pulse.KindSession && ev.Name == "daemon")
			job = job || ev.ID == "job:x"
		}
		return session && job
	}, 3*time.Second, 20*time.Millisecond, "the session node and the job must reach the spool")

	var surface string
	for _, m := range pulse.ListInstances(root) {
		if m.Instance == instance {
			surface = m.Surface
		}
	}
	assert.Equal(t, "daemon", surface)

	stop()
	assert.False(t, pulse.Default().Enabled(), "stop turns recording off")
	for _, ev := range pulse.Default().Snapshot() {
		assert.NotEqual(t, "daemon", ev.Name, "stop removes the session snapshotter")
	}
}
