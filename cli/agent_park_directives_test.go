/*
 * ChatCLI - Mid-park user directives tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent/park"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func newWaitingPark(t *testing.T, cli *ChatCLI) string {
	t.Helper()
	snap := &park.Snapshot{
		Token: park.NewToken(),
		Park:  park.Request{Mode: park.ModeDelay, Delay: 2 * time.Minute},
	}
	require.NoError(t, snap.Save())
	cli.registerActivePark(snap.Token, "18:44:09")
	return snap.Token
}

func TestParkNote_PersistsDirective(t *testing.T) {
	t.Setenv("CHATCLI_PARK_DIR", t.TempDir())
	cli := &ChatCLI{logger: zap.NewNop()}
	token := newWaitingPark(t, cli)

	cli.handleParkNoteCommand("seja mais detalhista na análise")
	cli.handleParkNoteCommand("inclua estatísticas de finalização")

	snap, err := park.Load(token)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"seja mais detalhista na análise",
		"inclua estatísticas de finalização",
	}, snap.PendingUserDirectives)
}

func TestParkNote_NoActivePark(t *testing.T) {
	t.Setenv("CHATCLI_PARK_DIR", t.TempDir())
	cli := &ChatCLI{logger: zap.NewNop()}

	// Must not panic and must not create anything — just the notice.
	cli.handleParkNoteCommand("mensagem sem destino")
	_, ok := cli.latestActivePark()
	assert.False(t, ok)
}

func TestParkNote_EmptyTextShowsUsage(t *testing.T) {
	t.Setenv("CHATCLI_PARK_DIR", t.TempDir())
	cli := &ChatCLI{logger: zap.NewNop()}
	token := newWaitingPark(t, cli)

	cli.handleParkNoteCommand("")

	snap, err := park.Load(token)
	require.NoError(t, err)
	assert.Empty(t, snap.PendingUserDirectives, "usage error must not record a directive")
}

func TestParkNote_StaleSnapshotDropsRegistryEntry(t *testing.T) {
	t.Setenv("CHATCLI_PARK_DIR", t.TempDir())
	cli := &ChatCLI{logger: zap.NewNop()}
	token := newWaitingPark(t, cli)
	require.NoError(t, park.Delete(token)) // cancelled behind our back

	cli.handleParkNoteCommand("oi")

	_, ok := cli.latestActivePark()
	assert.False(t, ok, "the stale registry entry must have been dropped")
}

func TestActiveParkRegistry_LatestWinsAndUnregister(t *testing.T) {
	cli := &ChatCLI{logger: zap.NewNop()}
	cli.registerActivePark("token-aaa-11111111", "10:00:00")
	cli.registerActivePark("token-bbb-22222222", "10:05:00")
	cli.registerActivePark("token-bbb-22222222", "10:05:00") // idempotent

	p, ok := cli.latestActivePark()
	require.True(t, ok)
	assert.Equal(t, "token-bbb-22222222", p.Token)

	cli.unregisterActivePark("token-bbb-22222222")
	p, ok = cli.latestActivePark()
	require.True(t, ok)
	assert.Equal(t, "token-aaa-11111111", p.Token)

	cli.unregisterActivePark("token-aaa-11111111")
	_, ok = cli.latestActivePark()
	assert.False(t, ok)
}

func TestMaybeShowParkNoteHint_OncePerPark(t *testing.T) {
	cli := &ChatCLI{logger: zap.NewNop()}
	cli.registerActivePark("token-aaa-11111111", "10:00:00")

	cli.maybeShowParkNoteHint()
	p, ok := cli.latestActivePark()
	require.True(t, ok)
	assert.True(t, p.HintShown, "first plain chat while parked marks the hint as shown")

	// Second call is a silent no-op (flag already set); a NEW park gets
	// its own hint.
	cli.maybeShowParkNoteHint()
	cli.registerActivePark("token-bbb-22222222", "10:05:00")
	p, ok = cli.latestActivePark()
	require.True(t, ok)
	assert.False(t, p.HintShown)
}

func TestBuildParkDirectivesMessage_ListsAllAndDemandsReport(t *testing.T) {
	msg := buildParkDirectivesMessage([]string{"seja mais detalhista", "inclua estatísticas"})

	assert.Contains(t, msg, "[user directives received while you were parked]")
	assert.Contains(t, msg, "- seja mais detalhista")
	assert.Contains(t, msg, "- inclua estatísticas")
	assert.Contains(t, msg, "THIS cycle's report")
}

func TestAppendDirective_QueueCap(t *testing.T) {
	t.Setenv("CHATCLI_PARK_DIR", t.TempDir())
	snap := &park.Snapshot{
		Token: park.NewToken(),
		Park:  park.Request{Mode: park.ModeDelay, Delay: time.Minute},
	}
	require.NoError(t, snap.Save())

	for i := 0; i < 20; i++ {
		require.NoError(t, park.AppendDirective(snap.Token, "d"))
	}
	assert.Error(t, park.AppendDirective(snap.Token, "overflow"),
		"the directive queue must be capped so a runaway session cannot balloon the snapshot")
}
