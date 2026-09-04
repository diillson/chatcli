/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package rpcserve

import (
	"testing"
)

// workspaceBackend layers the ACPWorkspaceBackend capability on the base fake.
type workspaceBackend struct {
	*fakeBackend
	adopted []string
}

func (w *workspaceBackend) AdoptWorkspace(dir string) {
	w.adopted = append(w.adopted, dir)
}

func TestACP_SessionNewAdoptsClientCwd(t *testing.T) {
	be := &workspaceBackend{fakeBackend: &fakeBackend{}}
	a := NewACP(be, "1.0.0")

	runLines(t, a.Handle,
		`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"/work/repo","mcpServers":[]}}`)

	if len(be.adopted) != 1 || be.adopted[0] != "/work/repo" {
		t.Fatalf("session/new must hand the client's cwd to the backend, got %v", be.adopted)
	}
}

func TestACP_SessionNewWithoutCwdAdoptsNothing(t *testing.T) {
	be := &workspaceBackend{fakeBackend: &fakeBackend{}}
	a := NewACP(be, "1.0.0")

	runLines(t, a.Handle,
		`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"   "}}`)

	if len(be.adopted) != 0 {
		t.Fatalf("an absent or blank cwd must not trigger an overlay, got %v", be.adopted)
	}
}

func TestACP_SessionNewWithoutWorkspaceCapabilityStillWorks(t *testing.T) {
	// The capability is optional: a backend that does not implement it must
	// keep serving session/new exactly as before.
	a := NewACP(&fakeBackend{}, "1.0.0")
	pr := runLines(t, a.Handle,
		`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"/work/repo"}}`)
	if len(pr) != 1 || pr[0].Error != nil {
		t.Fatalf("session/new must succeed without the capability: %+v", pr)
	}
}
