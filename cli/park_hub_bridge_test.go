/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent/park"
	"github.com/diillson/chatcli/server/hub"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func newParkBridgeCLI(t *testing.T) *ChatCLI {
	t.Helper()
	cli := &ChatCLI{logger: zap.NewNop(), unattended: true}
	cli.schedulerBridge = newSchedulerBridge(cli)
	return cli
}

func openParkBridgeStore(t *testing.T, ctx context.Context) *hub.SQLiteStore {
	t.Helper()
	store, err := hub.OpenSQLiteStore(ctx, filepath.Join(t.TempDir(), "hub.db"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// Owner side: the bridge heartbeats the parks this process owns, honors a
// wake request another process left for one of them (the in-turn waiter
// receives the outcome) and removes the row; a park that stops being
// owned loses its row.
func TestParkHubBridge_OwnerHeartbeatsAndHonorsRemoteWake(t *testing.T) {
	t.Setenv("CHATCLI_HUB_POLL_MS", "50")
	t.Setenv("CHATCLI_PARK_DIR", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := openParkBridgeStore(t, ctx)
	cli := newParkBridgeCLI(t)

	token := newWaitingPark(t, cli)
	waiter := cli.registerParkWaiter(token)
	defer cli.unregisterParkWaiter(token)

	stop := startParkHubBridge(ctx, store, cli, "gateway", nil)
	defer stop()

	if !waitFor(t, 3*time.Second, func() bool {
		recs, err := store.ListParkMirror(context.Background())
		return err == nil && len(recs) == 1 && recs[0].Token == token && recs[0].Origin == "gateway"
	}) {
		t.Fatal("owned park was not mirrored")
	}

	ok, err := store.RequestParkWake(context.Background(), token, "cancelled", "deploy already finished")
	require.NoError(t, err)
	require.True(t, ok)

	select {
	case out := <-waiter:
		if out.Outcome != "cancelled" || out.Detail != "deploy already finished" {
			t.Fatalf("waiter got %+v", out)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("remote wake request was not honored")
	}
	// The resumed turn releases the park (what runParkedInline does on wake):
	// the row goes and does not come back.
	cli.unregisterParkWaiter(token)
	cli.unregisterActivePark(token)
	if !waitFor(t, 3*time.Second, func() bool {
		recs, _ := store.ListParkMirror(context.Background())
		return len(recs) == 0
	}) {
		t.Fatal("row of a park no longer owned must be deleted")
	}

	// A park whose resume is already queued is no longer mirrored as
	// waiting, so a second cross-process cancel cannot queue a duplicate.
	queued := newWaitingPark(t, cli)
	cli.pendingResumeMu.Lock()
	cli.pendingResumeQueue = append(cli.pendingResumeQueue, queued)
	cli.pendingResumeMu.Unlock()
	time.Sleep(200 * time.Millisecond)
	if recs, _ := store.ListParkMirror(context.Background()); len(recs) != 0 {
		t.Fatalf("a park with a queued resume must not be mirrored: %+v", recs)
	}
}

// Canceller side: a park owned by another live process is cancelled there
// (wake request recorded, nothing resumed locally); a park whose owner
// stopped heartbeating is resumed locally.
func TestParkAdapter_CancelRoutesToLiveOwnerOrResumesLocally(t *testing.T) {
	t.Setenv("CHATCLI_HUB_POLL_MS", "50")
	t.Setenv("CHATCLI_PARK_DIR", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := openParkBridgeStore(t, ctx)
	cli := newParkBridgeCLI(t)
	stop := startParkHubBridge(ctx, store, cli, "repl", nil)
	defer stop()
	adapter := newLiveParkAdapter(cli)

	// Owned elsewhere, owner alive.
	snap := &park.Snapshot{Token: park.NewToken(), Park: park.Request{Mode: park.ModeDelay, Delay: time.Minute}}
	require.NoError(t, snap.Save())
	require.NoError(t, store.UpsertParkOwner(ctx, hub.ParkMirrorRecord{Token: snap.Token, Instance: "ffff", Origin: "gateway"}))

	out, err := adapter.CancelPark(snap.Token, "the deploy finished")
	require.NoError(t, err)
	if !strings.Contains(out, "via the hub") || !strings.Contains(out, "gateway") {
		t.Fatalf("expected the hub route, got %q", out)
	}
	recs, _ := store.ListParkMirror(ctx)
	require.Len(t, recs, 1)
	if recs[0].WakeOutcome != "cancelled" || recs[0].WakeDetail != "the deploy finished" {
		t.Fatalf("wake request not recorded: %+v", recs[0])
	}
	if cli.hasPendingResume(snap.Token) {
		t.Fatal("a park owned by a live process must not be resumed locally")
	}

	// Owned elsewhere, owner dead (stale heartbeat): resumed here.
	dead := &park.Snapshot{Token: park.NewToken(), Park: park.Request{Mode: park.ModeDelay, Delay: time.Minute}}
	require.NoError(t, dead.Save())
	require.NoError(t, store.UpsertParkOwner(ctx, hub.ParkMirrorRecord{
		Token: dead.Token, Instance: "eeee", Origin: "repl", HeartbeatAt: time.Now().Add(-time.Minute),
	}))
	out, err = adapter.CancelPark(dead.Token, "")
	require.NoError(t, err)
	if !strings.Contains(out, "no live process owns this park") || !cli.hasPendingResume(dead.Token) {
		t.Fatalf("dead owner: expected a local resume, got %q (pending=%v)", out, cli.hasPendingResume(dead.Token))
	}
}
