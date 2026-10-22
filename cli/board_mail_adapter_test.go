/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"path/filepath"
	"testing"

	"github.com/diillson/chatcli/cli/agent/mail"
	"github.com/diillson/chatcli/cli/board"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLiveBoardAdapter_EditDeleteUnassign(t *testing.T) {
	store := board.NewStore(filepath.Join(t.TempDir(), "board.json"))
	adapter := newLiveBoardAdapter(store)
	mutations := 0
	adapter.onMutate = func() { mutations++ }
	var _ plugins.BoardEditor = adapter

	created, err := adapter.Create("Implement /foo", "acceptance: tests", "coder", "")
	require.NoError(t, err)
	assert.Contains(t, created, "card-1")

	out, err := adapter.Edit("card-1", "Implement /foo v2", "")
	require.NoError(t, err)
	assert.Contains(t, out, "edited card-1")
	assert.Contains(t, out, `title="Implement /foo v2"`)

	_, err = adapter.Edit("card-1", "", "")
	require.ErrorIs(t, err, board.ErrNothingToChange)

	out, err = adapter.Unassign("card-1")
	require.NoError(t, err)
	assert.Contains(t, out, "unassigned card-1")
	assert.NotContains(t, out, "assignee=")

	_, err = adapter.Note("card-1", "reviewer", "looks good")
	require.NoError(t, err)

	out, err = adapter.Delete("card-1")
	require.NoError(t, err)
	assert.Contains(t, out, "deleted card-1 (permanently)")
	assert.Contains(t, out, "title=Implement /foo v2")
	assert.Contains(t, out, "description=acceptance: tests")
	assert.Contains(t, out, "note: [reviewer")
	_, err = store.Get("card-1")
	require.ErrorIs(t, err, board.ErrNotFound)

	_, err = adapter.Delete("card-1")
	require.Error(t, err)
	assert.Equal(t, 5, mutations, "create, edit, unassign, note, delete each fire the mutation tap")
}

func TestLiveMailAdapter_PeekIsNonDestructive(t *testing.T) {
	reg := mail.NewRegistry(10)
	adapter := newLiveMailAdapter(reg)
	var _ plugins.MailPeeker = adapter

	out, err := adapter.Peek("orchestrator")
	require.NoError(t, err)
	assert.Equal(t, "no pending mail for orchestrator", out)

	_, err = reg.Send("coder", "orchestrator", "card-2", "patch ready for review")
	require.NoError(t, err)

	out, err = adapter.Peek("Orchestrator")
	require.NoError(t, err)
	assert.Contains(t, out, "PENDING MAIL FOR Orchestrator (1")
	assert.Contains(t, out, "from=coder card=card-2")
	assert.Contains(t, out, "patch ready for review")

	// Still queued: the draining read gets it afterwards.
	drained, err := adapter.Inbox()
	require.NoError(t, err)
	assert.Contains(t, drained, "patch ready for review")
	out, _ = adapter.Peek("orchestrator")
	assert.Equal(t, "no pending mail for orchestrator", out)

	_, err = adapter.Peek("  ")
	require.Error(t, err)
}
