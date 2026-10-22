/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCanonicalTodoStatus(t *testing.T) {
	cases := map[string]string{
		"pending": "pending", "TODO": "pending", "In-Progress": "in_progress", "doing": "in_progress",
		"done": "completed", "Completed": "completed", "failed": "failed", "error": "failed",
		"blocked": "blocked", "waiting": "blocked", "on hold": "blocked",
		"cancelled": "cancelled", "canceled": "cancelled", "skipped": "cancelled",
		"": "", "bogus": "",
	}
	for in, want := range cases {
		if got := canonicalTodoStatus(in); got != want {
			t.Errorf("canonicalTodoStatus(%q) = %q, want %q", in, got, want)
		}
	}
}

// write accepts the new statuses (normalized) and keeps the reason; an
// unknown status is rejected naming the full vocabulary.
func TestTodoItemsFromPayload_BlockedCancelledAndReason(t *testing.T) {
	payload := map[string]json.RawMessage{
		"todos": json.RawMessage(`[{"description":"a","status":"Blocked","error":"needs creds"},{"description":"b","status":"canceled"},{"description":"c"}]`),
	}
	items, err := todoItemsFromPayload(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if items[0].Status != "blocked" || items[0].Error != "needs creds" || items[1].Status != "cancelled" || items[2].Status != "" {
		t.Fatalf("items: %+v", items)
	}
	payload["todos"] = json.RawMessage(`[{"description":"a","status":"later"}]`)
	if _, err := todoItemsFromPayload(payload); err == nil || !strings.Contains(err.Error(), todoStatusList) {
		t.Fatalf("unknown status must be rejected with the vocabulary, got %v", err)
	}
}

// The mark verb normalizes the status and accepts reason as an alias of
// error, so the adapter sees the canonical pair.
func TestTodoMark_NormalizesStatusAndReasonAlias(t *testing.T) {
	fake := &recordingTodoAdapter{}
	SetTodoAdapter(fake)
	defer SetTodoAdapter(nil)
	p := NewBuiltinTodoPlugin()
	if _, err := p.Execute(t.Context(), []string{`{"cmd":"mark","args":{"id":2,"status":"Waiting","reason":"user must approve"}}`}); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if fake.id != 2 || fake.status != "blocked" || fake.errMsg != "user must approve" {
		t.Fatalf("adapter saw id=%d status=%q err=%q", fake.id, fake.status, fake.errMsg)
	}
	if _, err := p.Execute(t.Context(), []string{`{"cmd":"mark","args":{"id":2,"status":"whatever"}}`}); err == nil || !strings.Contains(err.Error(), "status is required") {
		t.Fatalf("unknown status must be rejected, got %v", err)
	}
}

type recordingTodoAdapter struct {
	id     int
	status string
	errMsg string
}

func (r *recordingTodoAdapter) Write([]TodoItem) (string, error) { return "", nil }
func (r *recordingTodoAdapter) List() (string, error)            { return "", nil }
func (r *recordingTodoAdapter) Mark(id int, status, errMsg string) (string, error) {
	r.id, r.status, r.errMsg = id, status, errMsg
	return "ok", nil
}
