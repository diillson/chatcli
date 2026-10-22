/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package hub

import (
	"context"
	"testing"
	"time"
)

// A heartbeat upsert refreshes the owner row but must keep a pending wake
// request; the request lands only on a mirrored token; delete and purge
// remove rows.
func TestParkMirrorRoundtrip(t *testing.T) {
	store := openRunsTestStore(t)
	ctx := context.Background()

	if ok, err := store.RequestParkWake(ctx, "tok-1", "cancelled", "nobody owns it"); err != nil || ok {
		t.Fatalf("wake on unknown token: ok=%v err=%v", ok, err)
	}
	if err := store.UpsertParkOwner(ctx, ParkMirrorRecord{Token: "tok-1", Instance: "aaaa", Origin: "repl"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if ok, err := store.RequestParkWake(ctx, "tok-1", "cancelled", "deploy finished"); err != nil || !ok {
		t.Fatalf("wake request: ok=%v err=%v", ok, err)
	}
	if err := store.UpsertParkOwner(ctx, ParkMirrorRecord{Token: "tok-1", Instance: "aaaa", Origin: "repl"}); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	recs, err := store.ListParkMirror(ctx)
	if err != nil || len(recs) != 1 {
		t.Fatalf("list: %d err=%v", len(recs), err)
	}
	if recs[0].WakeOutcome != "cancelled" || recs[0].WakeDetail != "deploy finished" || recs[0].WakeRequestedAt.IsZero() {
		t.Fatalf("heartbeat must keep the pending wake: %+v", recs[0])
	}
	if time.Since(recs[0].HeartbeatAt) > time.Minute {
		t.Fatalf("heartbeat not refreshed: %v", recs[0].HeartbeatAt)
	}
	if err := store.DeleteParkMirror(ctx, "tok-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if recs, _ := store.ListParkMirror(ctx); len(recs) != 0 {
		t.Fatalf("row must be gone, got %d", len(recs))
	}

	stale := ParkMirrorRecord{Token: "tok-2", Instance: "bbbb", HeartbeatAt: time.Now().Add(-time.Hour)}
	if err := store.UpsertParkOwner(ctx, stale); err != nil {
		t.Fatalf("upsert stale: %v", err)
	}
	if n, err := store.PurgeParkMirror(ctx, 5*time.Minute); err != nil || n != 1 {
		t.Fatalf("purge: n=%d err=%v", n, err)
	}
}

// The activity column: an empty Activity on upsert keeps the stored tail
// (heartbeats never erase it); a non-empty one replaces it.
func TestAgentRunActivityKeptOnEmptyUpsert(t *testing.T) {
	store := openRunsTestStore(t)
	ctx := context.Background()
	rec := AgentRunRecord{RunID: "run-cccc-1", Instance: "cccc", Status: "running", Payload: "{}", Activity: `[{"text":"turn 1"}]`}
	if err := store.UpsertAgentRun(ctx, rec); err != nil {
		t.Fatalf("insert: %v", err)
	}
	rec.Activity = ""
	if err := store.UpsertAgentRun(ctx, rec); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	recs, _ := store.ListAgentRuns(ctx)
	if len(recs) != 1 || recs[0].Activity != `[{"text":"turn 1"}]` {
		t.Fatalf("activity must survive an empty upsert: %+v", recs)
	}
	rec.Activity = `[{"text":"turn 2"}]`
	if err := store.UpsertAgentRun(ctx, rec); err != nil {
		t.Fatalf("update: %v", err)
	}
	if recs, _ := store.ListAgentRuns(ctx); recs[0].Activity != `[{"text":"turn 2"}]` {
		t.Fatalf("activity must be replaced when sent: %+v", recs)
	}
}

// A database created before the activity column existed is migrated on
// open: the column is added once and a second open is a no-op.
func TestEnsureColumnMigratesOldAgentRunsTable(t *testing.T) {
	store := openRunsTestStore(t)
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, `CREATE TABLE legacy (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("legacy table: %v", err)
	}
	if err := ensureColumn(ctx, store.db, "legacy", "activity", "TEXT NOT NULL DEFAULT ''"); err != nil {
		t.Fatalf("add column: %v", err)
	}
	if err := ensureColumn(ctx, store.db, "legacy", "activity", "TEXT NOT NULL DEFAULT ''"); err != nil {
		t.Fatalf("second ensure must be a no-op: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO legacy (id, activity) VALUES ('x', 'y')`); err != nil {
		t.Fatalf("column unusable: %v", err)
	}
}
