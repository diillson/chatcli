/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const jsonToolFixture = `{"data":{"items":[{"id":1,"name":"alpha","status":"active","tags":["x"]},{"id":2,"name":"beta","status":"failed"},{"id":3,"name":"gamma","status":"active","meta":{"k":true}}],"count":3},"ok":true,"big":12345678901234567890}`

// jsonToolWorkspace chdirs into a temp dir (the engine's workspace boundary)
// and writes the fixture there; returns the file name.
func jsonToolWorkspace(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return name
}

func runJSON(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return NewBuiltinJSONPlugin().Execute(context.Background(), args)
}

func TestJSONTool_Verbs(t *testing.T) {
	file := jsonToolWorkspace(t, "resp.json", jsonToolFixture)
	cases := []struct {
		name, args string
		want       []string
		wantErr    string
	}{
		{"get scalar", `{"cmd":"get","args":{"file":"resp.json","path":"data.items[1].name"}}`, []string{`"beta"`}, ""},
		{"get negative index", `{"cmd":"get","args":{"file":"resp.json","path":"data.items[-1].id"}}`, []string{"3"}, ""},
		{"get wildcard", `{"cmd":"get","args":{"file":"resp.json","path":"data.items[*].name"}}`, []string{"3 matches", `"alpha"`, `"gamma"`}, ""},
		{"get wildcard skips missing", `{"cmd":"get","args":{"file":"resp.json","path":"data.items.*.meta.k"}}`, []string{"true"}, ""},
		{"get big number verbatim", `{"cmd":"get","args":{"file":"resp.json","path":"big"}}`, []string{"12345678901234567890"}, ""},
		{"get missing key lists keys", `{"cmd":"get","args":{"file":"resp.json","path":"data.nope"}}`, nil, `key "nope" not found at root.data (keys: count, items)`},
		{"get index on object", `{"cmd":"get","args":{"file":"resp.json","path":"data.items.x"}}`, nil, "is an array of 3"},
		{"get out of range", `{"cmd":"get","args":{"file":"resp.json","path":"data.items[9]"}}`, nil, "index 9 out of range"},
		{"keys root", `{"cmd":"keys","args":{"file":"resp.json"}}`, []string{"object with 3 keys", "data: object{2}", "ok: bool"}, ""},
		{"keys array", `{"cmd":"keys","args":{"file":"resp.json","path":"data.items"}}`, []string{"array of 3", "object×3"}, ""},
		{"query eq select", `{"cmd":"query","args":{"file":"resp.json","path":"data.items","where":"status=active","select":["id"]}}`, []string{"2 of 3 items match", `"id": 1`, `"id": 3`}, ""},
		{"query regex and limit", `{"cmd":"query","args":{"file":"resp.json","path":"data.items","where":"name~^(alpha|gamma)$ && status!=failed","limit":1}}`, []string{"2 of 3 items match", "showing 1"}, ""},
		{"query no match", `{"cmd":"query","args":{"file":"resp.json","path":"data.items","where":"status=gone"}}`, []string{"0 of 3 items match"}, ""},
		{"query bad where", `{"cmd":"query","args":{"file":"resp.json","path":"data.items","where":"status"}}`, nil, `must be "field=value"`},
		{"schema", `{"cmd":"schema","args":{"file":"resp.json"}}`, []string{"object {3 keys}", "items: array [3] of object×3", "meta (1/3): object {1 keys}", "tags (1/3): array [1] of string×1"}, ""},
		{"schema depth 1", `{"cmd":"schema","args":{"file":"resp.json","depth":1}}`, []string{"data: object {2 keys}"}, ""},
		{"validate ok", `{"cmd":"validate","args":{"file":"resp.json"}}`, []string{"valid json: object{3}"}, ""},
		{"pretty compact", `{"cmd":"pretty","args":{"text":"{\"a\": [1, 2]}","compact":true}}`, []string{`{"a":[1,2]}`}, ""},
		{"flat args default get", `{"file":"resp.json","path":"ok"}`, []string{"true"}, ""},
		{"flat args default keys", `{"file":"resp.json"}`, []string{"object with 3 keys"}, ""},
		{"argv form", "get resp.json --path data.count", []string{"3"}, ""},
		{"unknown cmd", `{"cmd":"explode","args":{"file":"resp.json"}}`, nil, `unknown cmd "explode"`},
		{"missing input", `{"cmd":"keys"}`, nil, `"file" or "text" is required`},
		{"file is dir", `{"cmd":"keys","args":{"file":"."}}`, nil, "is a directory"},
	}
	_ = file
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := runJSON(t, strings.Fields(c.args)...)
			if strings.HasPrefix(c.args, "{") {
				out, err = runJSON(t, c.args)
			}
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("want error containing %q, got err=%v out=%q", c.wantErr, err, out)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for _, w := range c.want {
				if !strings.Contains(out, w) {
					t.Errorf("output missing %q:\n%s", w, out)
				}
			}
		})
	}
}

func TestJSONTool_YAMLAndErrors(t *testing.T) {
	jsonToolWorkspace(t, "values.yaml", "replicas: 2\nimage:\n  tag: v1\nports:\n  - 80\n  - 443\n")
	out, err := runJSON(t, `{"cmd":"get","args":{"file":"values.yaml","path":"image.tag"}}`)
	if err != nil || !strings.Contains(out, `"v1"`) {
		t.Fatalf("yaml get: %v %q", err, out)
	}
	out, err = runJSON(t, `{"cmd":"keys","args":{"text":"a: 1\nb: [1, 2]\n"}}`)
	if err != nil || !strings.Contains(out, "b: array[2]") {
		t.Fatalf("inline yaml keys: %v %q", err, out)
	}
	out, err = runJSON(t, `{"cmd":"validate","args":{"text":"{\"a\": 1,\n  \"b\": }"}}`)
	if err != nil || !strings.Contains(out, "invalid: inline text — invalid JSON at line 2, column 8") {
		t.Fatalf("validate must report line/column: %v %q", err, out)
	}
	if _, err = runJSON(t, `{"cmd":"get","args":{"text":"{\"a\":1} trailing","path":"a"}}`); err == nil || !strings.Contains(err.Error(), "trailing data") {
		t.Fatalf("trailing data must be rejected: %v", err)
	}
	if _, err = runJSON(t, `{"cmd":"get","args":{"text":"just words","path":"a"}}`); err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("plain prose is neither JSON nor a YAML document: %v", err)
	}
}

func TestJSONTool_BoundsAndWorkspace(t *testing.T) {
	jsonToolWorkspace(t, "small.json", "{}")
	outside := filepath.Join(t.TempDir(), "secret.json")
	if err := os.WriteFile(outside, []byte(`{"k":"v"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runJSON(t, `{"cmd":"keys","args":{"file":"`+outside+`"}}`); err == nil || !strings.Contains(err.Error(), "outside workspace boundary") {
		t.Fatalf("read outside the workspace must be refused: %v", err)
	}
	// A huge array is cut on a rune boundary with a hint, never an error.
	var sb strings.Builder
	sb.WriteString("[")
	for i := 0; i < 4000; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`"ação-` + strings.Repeat("x", 10) + `"`)
	}
	sb.WriteString("]")
	out, err := runJSON(t, `{"cmd":"pretty","args":{"text":`+quoteJSON(sb.String())+`}}`)
	if err != nil || len(out) > jsonToolOutputCap+200 || !strings.Contains(out, "[output cut:") || !strings.Contains(out, jsonToolCutHint) {
		t.Fatalf("output cap: err=%v len=%d tail=%q", err, len(out), out[max(0, len(out)-120):])
	}
}

func quoteJSON(s string) string {
	return renderJSON(s, true)
}
