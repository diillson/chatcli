/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package rpcserve

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// searchFake adds the SessionSearchBackend capability on top of proxyFake.
type searchFake struct {
	proxyFake
	lastSearch string
	lastFork   string
}

func (f *searchFake) SearchSessions(_ context.Context, query string) (string, error) {
	f.lastSearch = query
	return "search-hits:" + query, nil
}

func (f *searchFake) ForkSession(_ context.Context, source, target string) (string, error) {
	f.lastFork = source + "->" + target
	return "forked:" + source + ":" + target, nil
}

// TestMCP_SessionSearchAndFork pins the additive capability: advertised in the
// manage_session schema only when the backend implements SessionSearchBackend,
// dispatched with query/name/to, and parameter-validated.
func TestMCP_SessionSearchAndFork(t *testing.T) {
	be := &searchFake{}
	m := NewMCP(be, "chatcli", "1.0.0")

	resps := runLines(t, m.Handle, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	b, _ := json.Marshal(resps[0].Result)
	for _, want := range []string{"search", "fork", `"query"`, `"to"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("search-capable backend must advertise %q in manage_session: %s", want, b)
		}
	}

	r := runLines(t, m.Handle,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"manage_session","arguments":{"action":"search","query":"kafka outage"}}}`,
	)
	if rb, _ := json.Marshal(r[0].Result); !strings.Contains(string(rb), "search-hits:kafka outage") {
		t.Errorf("search dispatch wrong: %s", rb)
	}

	r = runLines(t, m.Handle,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"manage_session","arguments":{"action":"fork","name":"orig","to":"copy"}}}`,
	)
	if rb, _ := json.Marshal(r[0].Result); !strings.Contains(string(rb), "forked:orig:copy") {
		t.Errorf("fork dispatch wrong: %s", rb)
	}

	// Missing parameters are invalid-params, not silent fallthrough.
	r = runLines(t, m.Handle,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"manage_session","arguments":{"action":"search"}}}`,
	)
	if r[0].Error == nil || r[0].Error.Code != CodeInvalidParams {
		t.Errorf("search without query must be invalid params: %+v", r[0])
	}
	r = runLines(t, m.Handle,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"manage_session","arguments":{"action":"fork","name":"orig"}}}`,
	)
	if r[0].Error == nil || r[0].Error.Code != CodeInvalidParams {
		t.Errorf("fork without target must be invalid params: %+v", r[0])
	}

	// A backend WITHOUT the capability routes search through the classic
	// ManageSession (which reports the unknown action itself) and does not
	// advertise query/to.
	classic := proxyBackend()
	mc := NewMCP(classic, "chatcli", "1.0.0")
	resps = runLines(t, mc.Handle, `{"jsonrpc":"2.0","id":6,"method":"tools/list","params":{}}`)
	if cb, _ := json.Marshal(resps[0].Result); strings.Contains(string(cb), `"to"`) {
		t.Errorf("classic backend must not advertise the fork target parameter: %s", cb)
	}
	_ = runLines(t, mc.Handle,
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"manage_session","arguments":{"action":"search","query":"x"}}}`,
	)
	if classic.lastSessionOp != "search:mcp:" {
		t.Errorf("classic backend should receive the action verbatim: %q", classic.lastSessionOp)
	}
}
