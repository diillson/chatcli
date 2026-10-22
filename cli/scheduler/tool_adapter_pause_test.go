/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package scheduler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestToolAdapterPauseResume(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.AuditEnabled = false
	cfg.SnapshotInterval = 0
	cfg.WALGCInterval = 0
	cfg.DaemonAutoConnect = false
	s, err := New(cfg, NewNoopBridge(), SchedulerDeps{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}

	owner := Owner{Kind: OwnerAgent, ID: "agent-1"}
	ta := NewToolAdapter(s)
	out, err := ta.ScheduleJob(ctx, owner, `{"name":"later","when":"+1h","do":"noop"}`)
	if err != nil {
		t.Fatal(err)
	}
	var created struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil || created.JobID == "" {
		t.Fatalf("no job id in %q (%v)", out, err)
	}
	id := created.JobID

	out, err = ta.PauseJob(ctx, owner, `{"id":"`+id+`","reason":"hold"}`)
	if err != nil || !strings.Contains(out, `"paused"`) {
		t.Fatalf("pause: %q %v", out, err)
	}
	j, _ := s.Query(JobID(id))
	if j.Status != StatusPaused || j.PauseReason != "hold" {
		t.Fatalf("job after pause: %s %q", j.Status, j.PauseReason)
	}
	out, err = ta.ResumeJob(ctx, owner, `{"id":"`+id+`"}`)
	if err != nil || !strings.Contains(out, `"pending"`) {
		t.Fatalf("resume: %q %v", out, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if j, _ := s.Query(JobID(id)); j.Status == StatusPending {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if j, _ := s.Query(JobID(id)); j.Status != StatusPending {
		t.Fatalf("job after resume: %s", j.Status)
	}
	if out, _ := ta.PauseJob(ctx, owner, `{}`); !strings.Contains(out, "id is required") {
		t.Fatalf("pause without id: %q", out)
	}
	if out, _ := ta.ResumeJob(ctx, Owner{Kind: OwnerAgent, ID: "someone-else"}, `{"id":"`+id+`"}`); !strings.Contains(out, "error") {
		t.Fatalf("another owner must not resume: %q", out)
	}
}
