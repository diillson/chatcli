/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package rpcserve

import (
	"context"
	"sort"
	"sync"
	"testing"

	"github.com/diillson/chatcli/cli/agent/proc"
)

// ownerBackend records which process table a direct tool call was made for.
type ownerBackend struct {
	*fakeBackend
	mu     sync.Mutex
	owners []string
}

func (b *ownerBackend) CallTool(ctx context.Context, _, _ string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.owners = append(b.owners, proc.OwnerFrom(ctx))
	return "ok", nil
}

// An MCP tools/call of a plugin tool runs for the session it names, and for
// the default MCP session when it names none — the same one the agent runs
// of that client use — never on the terminal's table.
func TestMCPDirectToolCallCarriesItsSession(t *testing.T) {
	be := &ownerBackend{fakeBackend: &fakeBackend{}}
	m := NewMCP(be, "chatcli", "1.0.0")
	runLines(t, m.Handle,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read","arguments":{"args":"y","session":"s9"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"read","arguments":{"args":"y"}}}`,
	)
	sort.Strings(be.owners) // requests are served concurrently
	if len(be.owners) != 2 || be.owners[0] != "session:mcp" || be.owners[1] != "session:s9" {
		t.Fatalf("direct tool calls ran on the tables of %q", be.owners)
	}
}
