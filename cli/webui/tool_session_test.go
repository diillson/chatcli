/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package webui

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent/proc"
)

// ownerBackend records which process table a direct tool call was made for.
type ownerBackend struct {
	*fakeBackend
	owners chan string
}

func (b *ownerBackend) CallTool(ctx context.Context, _, _ string) (string, error) {
	b.owners <- proc.OwnerFrom(ctx)
	return "tool-out", nil
}

// A tool the user types in a web session ("@read x"), and one called on the
// tools endpoint with a session, run for THAT session: its @proc must see
// the processes its agent started, and not the terminal's or another
// session's.
func TestDirectToolCallsCarryTheWebSession(t *testing.T) {
	be := &ownerBackend{fakeBackend: newFakeBackend(), owners: make(chan string, 4)}
	srv, err := Start(Options{Backend: be, PermissionTimeout: 2 * time.Second, Version: "t"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	for name, c := range map[string]struct {
		path string
		body map[string]string
		want string
	}{
		"inline @tool in a turn": {"/api/turn", map[string]string{"mode": "chat", "text": "@read x", "session": "s1"}, "session:s1"},
		"tools endpoint":         {"/api/tools/read", map[string]string{"args": "x", "session": "s1"}, "session:s1"},
		// No session named: the page's default session, as a turn resolves
		// it — the same table for the tools endpoint and for turns.
		"inline @tool, default session": {"/api/turn", map[string]string{"mode": "chat", "text": "@read x"}, "session:web"},
		"tools endpoint, no session":    {"/api/tools/read", map[string]string{"args": "x"}, "session:web"},
		"tools endpoint, blank session": {"/api/tools/read", map[string]string{"args": "x", "session": "  "}, "session:web"},
	} {
		if status, body := call(t, srv, http.MethodPost, c.path, c.body, true); status != http.StatusOK {
			t.Fatalf("%s: status %d %s", name, status, body)
		}
		select {
		case got := <-be.owners:
			if got != c.want {
				t.Errorf("%s ran on the table of %q, want %q", name, got, c.want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s never reached the backend", name)
		}
	}
}
