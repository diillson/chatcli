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

func TestCaptureParkDirective_PersistsAndEchoes(t *testing.T) {
	t.Setenv("CHATCLI_PARK_DIR", t.TempDir())
	cli := &ChatCLI{logger: zap.NewNop()}
	token := newWaitingPark(t, cli)

	captured := cli.captureParkDirective("seja mais detalhista na análise")

	require.True(t, captured, "plain input with a waiting park must be captured, not sent to chat")
	snap, err := park.Load(token)
	require.NoError(t, err)
	require.Equal(t, []string{"seja mais detalhista na análise"}, snap.PendingUserDirectives)

	// Second directive appends.
	require.True(t, cli.captureParkDirective("inclua estatísticas de finalização"))
	snap, err = park.Load(token)
	require.NoError(t, err)
	assert.Len(t, snap.PendingUserDirectives, 2)
}

func TestCaptureParkDirective_NoActivePark(t *testing.T) {
	t.Setenv("CHATCLI_PARK_DIR", t.TempDir())
	cli := &ChatCLI{logger: zap.NewNop()}

	assert.False(t, cli.captureParkDirective("mensagem de chat normal"))
}

func TestCaptureParkDirective_StaleRegistryFallsBackToChat(t *testing.T) {
	t.Setenv("CHATCLI_PARK_DIR", t.TempDir())
	cli := &ChatCLI{logger: zap.NewNop()}
	token := newWaitingPark(t, cli)
	require.NoError(t, park.Delete(token)) // cancelled behind our back

	captured := cli.captureParkDirective("oi")

	assert.False(t, captured, "with the snapshot gone the input must degrade to chat")
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
