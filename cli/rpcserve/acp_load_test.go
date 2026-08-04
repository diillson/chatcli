/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package rpcserve

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// restoreBackend layers the ACPSessionRestoreBackend capability on top of the
// base fake — kept separate so every other test keeps exercising the
// capability-less path (loadSession:false).
type restoreBackend struct {
	*fakeBackend
	items map[string][]HistoryItem
}

func (r *restoreBackend) RestoreSession(_ context.Context, session string) ([]HistoryItem, error) {
	items, ok := r.items[session]
	if !ok {
		return nil, fmt.Errorf("unknown session %q", session)
	}
	return items, nil
}

func TestACP_InitializeAdvertisesLoadSessionByCapability(t *testing.T) {
	// Base backend: no capability → false.
	a := NewACP(&fakeBackend{}, "1.0.0")
	pr := runLines(t, a.Handle, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	res, _ := json.Marshal(pr[0].Result)
	if !strings.Contains(string(res), `"loadSession":false`) {
		t.Errorf("capability-less backend must advertise loadSession:false, got %s", res)
	}

	// Restore-capable backend → true.
	a = NewACP(&restoreBackend{fakeBackend: &fakeBackend{}, items: nil}, "1.0.0")
	pr = runLines(t, a.Handle, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	res, _ = json.Marshal(pr[0].Result)
	if !strings.Contains(string(res), `"loadSession":true`) {
		t.Errorf("restore-capable backend must advertise loadSession:true, got %s", res)
	}
}

func TestACP_SessionLoadReplaysHistoryThenCommands(t *testing.T) {
	be := &restoreBackend{
		fakeBackend: &fakeBackend{},
		items: map[string][]HistoryItem{
			"old-1": {
				{Role: "user", Content: "first question"},
				{Role: "assistant", Content: "first answer"},
			},
		},
	}
	a := NewACP(be, "1.0.0")
	notes, nmu := notesRecorder(a)

	pr := runLines(t, a.Handle,
		`{"jsonrpc":"2.0","id":1,"method":"session/load","params":{"sessionId":"old-1","cwd":"/tmp"}}`,
	)
	res, _ := json.Marshal(pr[0].Result)
	if !strings.Contains(string(res), "availableModes") {
		t.Errorf("session/load result must carry modes, got %s", res)
	}

	nmu.Lock()
	joined := strings.Join(*notes, "\n")
	nmu.Unlock()
	// Replay: the user turn as user_message_chunk, the reply as
	// agent_message_chunk, then the command surface re-advertised.
	for _, marker := range []string{
		`user_message_chunk`, `first question`,
		`agent_message_chunk`, `first answer`,
		`available_commands_update`,
	} {
		if !strings.Contains(joined, marker) {
			t.Errorf("expected replay frames to contain %q, got:\n%s", marker, joined)
		}
	}
	// The user chunk must precede the assistant chunk.
	if strings.Index(joined, "first question") > strings.Index(joined, "first answer") {
		t.Error("replay must preserve conversation order")
	}

	// The restored session must accept prompts (it is registered).
	pr = runLines(t, a.Handle,
		`{"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"sessionId":"old-1","prompt":[{"type":"text","text":"continue"}]}}`,
	)
	res, _ = json.Marshal(pr[0].Result)
	if !strings.Contains(string(res), "end_turn") {
		t.Errorf("prompt on restored session must run, got %s", res)
	}

	// Unknown session id → invalid params, no replay.
	pr = runLines(t, a.Handle,
		`{"jsonrpc":"2.0","id":3,"method":"session/load","params":{"sessionId":"ghost"}}`,
	)
	if pr[0].Error == nil {
		t.Fatal("session/load of an unknown id must error")
	}
}
