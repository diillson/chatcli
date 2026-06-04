/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestArgvToInnerJSON_Flags(t *testing.T) {
	// Mirrors what the agent emits for {cmd:create, args:{name, triggers:[...]}}.
	argv := []string{"--name", "deploy-x", "--description", "How to deploy", "--triggers", "deploy x", "--triggers", "ship x", "--allowed_tools", "@coder"}
	pos, inner := argvToInnerJSON(argv, map[string]bool{"triggers": true, "allowed_tools": true}, nil)
	if pos != "" {
		t.Fatalf("unexpected positional %q", pos)
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(inner), &m); err != nil {
		t.Fatal(err)
	}
	if m["name"] != "deploy-x" || m["description"] != "How to deploy" {
		t.Fatalf("scalars wrong: %v", m)
	}
	tr, _ := json.Marshal(m["triggers"])
	if string(tr) != `["deploy x","ship x"]` {
		t.Fatalf("triggers wrong: %s", tr)
	}
	// single value of an array key still becomes an array
	at, _ := json.Marshal(m["allowed_tools"])
	if string(at) != `["@coder"]` {
		t.Fatalf("allowed_tools should be array: %s", at)
	}
}

func TestArgvToInnerJSON_BareFlagAndPositional(t *testing.T) {
	pos, inner := argvToInnerJSON([]string{"deploy-x"}, nil, nil)
	if pos != "deploy-x" || inner != "{}" {
		t.Fatalf("positional case: pos=%q inner=%q", pos, inner)
	}
	_, inner = argvToInnerJSON([]string{"--overwrite", "--path", "p.json"}, nil, nil)
	var m map[string]interface{}
	_ = json.Unmarshal([]byte(inner), &m)
	if m["overwrite"] != true || m["path"] != "p.json" {
		t.Fatalf("bare flag / value wrong: %v", m)
	}
}

func TestArgvInner_PrimaryMapping(t *testing.T) {
	// no flags → positional maps to primary key
	if got := argvInner([]string{"rate", "limiter"}, "query", nil, nil); got != `{"query":"rate limiter"}` {
		t.Fatalf("primary mapping: %s", got)
	}
	// flags present → flag object wins
	got := argvInner([]string{"--query", "rate limiter"}, "query", nil, nil)
	if !reflect.DeepEqual(mustMap(got), map[string]interface{}{"query": "rate limiter"}) {
		t.Fatalf("flag query: %s", got)
	}
}

func mustMap(s string) map[string]interface{} {
	var m map[string]interface{}
	_ = json.Unmarshal([]byte(s), &m)
	return m
}
