/*
 * ChatCLI - ACP available-commands refresh tests.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 */
package rpcserve

import (
	"sync"
	"testing"
)

// mutableCmdBackend lets the test change the advertised command list
// mid-session, simulating command files edited (or /reload) while an IDE
// session is open.
type mutableCmdBackend struct {
	*fakeBackend
	mu   sync.Mutex
	cmds []CommandInfo
}

func (b *mutableCmdBackend) ACPCommands() []CommandInfo {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]CommandInfo(nil), b.cmds...)
}

func (b *mutableCmdBackend) setCmds(cmds []CommandInfo) {
	b.mu.Lock()
	b.cmds = cmds
	b.mu.Unlock()
}

func newRefreshACP(backend ACPBackend) (*ACP, *[]string) {
	a := &ACP{backend: backend, sessions: map[string]*acpSession{"s1": {mode: "chat"}}}
	var sent []string
	a.notify = func(method string, params interface{}) error {
		sent = append(sent, method)
		return nil
	}
	return a, &sent
}

func TestRefreshAvailableCommands_OnlyOnChange(t *testing.T) {
	backend := &mutableCmdBackend{fakeBackend: &fakeBackend{},
		cmds: []CommandInfo{{Name: "standup", Description: "Standup summary", InputHint: "[dias]"}}}
	a, sent := newRefreshACP(backend)

	// First advertise records the signature.
	a.sendAvailableCommands("s1")
	if len(*sent) != 1 {
		t.Fatalf("initial advertise expected, got %v", *sent)
	}

	// Unchanged catalog: refresh must stay silent.
	a.refreshAvailableCommands("s1")
	if len(*sent) != 1 {
		t.Fatalf("unchanged catalog must not re-notify, got %d sends", len(*sent))
	}

	// Catalog mutated mid-session (file edited / /reload): one re-advertise.
	backend.setCmds([]CommandInfo{
		{Name: "standup", Description: "Standup summary", InputHint: "[dias]"},
		{Name: "release", Description: "Cut a release"},
	})
	a.refreshAvailableCommands("s1")
	if len(*sent) != 2 {
		t.Fatalf("changed catalog must re-advertise exactly once, got %d sends", len(*sent))
	}
	// And it settles again.
	a.refreshAvailableCommands("s1")
	if len(*sent) != 2 {
		t.Fatalf("post-refresh catalog must be quiet again, got %d sends", len(*sent))
	}
}

func TestRefreshAvailableCommands_NoCapabilityNoPanic(t *testing.T) {
	// noCmdBackend deliberately hides the ACPCommandBackend capability.
	a, sent := newRefreshACP(&noCmdBackend{f: &fakeBackend{}})
	a.refreshAvailableCommands("s1")
	if len(*sent) != 0 {
		t.Fatalf("backend without the capability must be a no-op, got %v", *sent)
	}
}
