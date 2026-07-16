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

// resourceFake adds the ResourceBackend capability on top of fakeBackend.
type resourceFake struct {
	fakeBackend
}

func (f *resourceFake) Resources() []ResourceInfo {
	return []ResourceInfo{
		{URI: "chatcli://memory/index", Name: "memory-index", Description: "memory index"},
		{URI: "chatcli://skills", Name: "skills", Description: "skill catalog", MimeType: "application/json"},
	}
}

func (f *resourceFake) ReadResource(_ context.Context, uri string) (ResourceContent, error) {
	if uri == "chatcli://memory/index" {
		return ResourceContent{URI: uri, Text: "MEMORY-INDEX-BODY"}, nil
	}
	return ResourceContent{}, errUnknownSkill
}

// TestMCP_ResourcesSurface pins the optional capability: advertised in
// initialize, listed and readable only when the backend implements
// ResourceBackend; a slim backend keeps method-not-found semantics.
func TestMCP_ResourcesSurface(t *testing.T) {
	m := NewMCP(&resourceFake{}, "chatcli", "1.0.0")

	resps := runLines(t, m.Handle,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`,
	)
	b, _ := json.Marshal(resps[0].Result)
	if !strings.Contains(string(b), `"resources"`) {
		t.Errorf("resource-capable backend must advertise resources in initialize: %s", b)
	}

	resps = runLines(t, m.Handle, `{"jsonrpc":"2.0","id":2,"method":"resources/list","params":{}}`)
	b, _ = json.Marshal(resps[0].Result)
	for _, want := range []string{"chatcli://memory/index", "chatcli://skills", "application/json", "text/plain"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("resources/list missing %q: %s", want, b)
		}
	}

	resps = runLines(t, m.Handle,
		`{"jsonrpc":"2.0","id":3,"method":"resources/read","params":{"uri":"chatcli://memory/index"}}`,
	)
	b, _ = json.Marshal(resps[0].Result)
	if !strings.Contains(string(b), "MEMORY-INDEX-BODY") {
		t.Errorf("resources/read must return the content: %s", b)
	}

	// Unknown URI and missing URI are invalid-params.
	resps = runLines(t, m.Handle,
		`{"jsonrpc":"2.0","id":4,"method":"resources/read","params":{"uri":"chatcli://nope"}}`,
		`{"jsonrpc":"2.0","id":5,"method":"resources/read","params":{}}`,
	)
	for i, r := range resps {
		if r.Error == nil || r.Error.Code != CodeInvalidParams {
			t.Errorf("resp %d: expected invalid params, got %+v", i, r)
		}
	}

	// Slim backend: no capability, no methods.
	slim := NewMCP(&fakeBackend{}, "chatcli", "1.0.0")
	resps = runLines(t, slim.Handle,
		`{"jsonrpc":"2.0","id":6,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`,
		`{"jsonrpc":"2.0","id":7,"method":"resources/list","params":{}}`,
	)
	if b, _ := json.Marshal(resps[0].Result); strings.Contains(string(b), `"resources"`) {
		t.Errorf("slim backend must not advertise resources: %s", b)
	}
	if resps[1].Error == nil || resps[1].Error.Code != CodeMethodNotFound {
		t.Errorf("slim backend resources/list must be method-not-found: %+v", resps[1])
	}
}
