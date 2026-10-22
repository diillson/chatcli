/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/plugins"
)

// A builtin without subcommands documents its arguments at the top level;
// they must reach the model, in the catalog and in @tools describe.
func TestToolBlockRendersTopLevelFlags(t *testing.T) {
	recall := plugins.NewBuiltinRecallPlugin()
	full := renderToolBlock(recall, false)
	for _, want := range []string{
		"- Tool: @recall (v" + recall.Version() + ")", "Flags:", "- key (string) [required]",
		"- offset (integer)", "- max_chars (integer)", "- match (string)", "Exemplos:", `{"key":"THE_KEY","match":"error"}`,
	} {
		if !strings.Contains(full, want) {
			t.Fatalf("full block lacks %q:\n%s", want, full)
		}
	}
	if strings.Contains(full, "Available subcommands:") {
		t.Fatalf("a tool without subcommands must not print an empty subcommand list:\n%s", full)
	}
	compact := renderToolBlock(recall, true)
	if !strings.Contains(compact, "Required: key (string)") || !strings.Contains(compact, `{"key":"THE_KEY"}`) ||
		strings.Contains(compact, "- offset (integer)") {
		t.Fatalf("compact block keeps the required args and one example:\n%s", compact)
	}
	for _, p := range []plugins.Plugin{plugins.NewBuiltinCommandsPlugin(), plugins.NewBuiltinCompressPlugin(), plugins.NewBuiltinGraphViewPlugin()} {
		if b := renderToolBlock(p, false); !strings.Contains(b, "Flags:") {
			t.Errorf("%s: flags missing:\n%s", p.Name(), b)
		}
	}
	if b := renderToolBlock(plugins.NewBuiltinAskPlugin(), false); !strings.Contains(b, "Exemplos:") || !strings.Contains(b, "Formato args:") {
		t.Errorf("@ask: format and examples must reach the model:\n%s", b)
	}
	if b := renderToolBlock(plugins.NewBuiltinChannelsPlugin(), false); !strings.Contains(b, "Available subcommands:") {
		t.Errorf("@channels keeps its subcommands:\n%s", b)
	}
	if line := renderToolIndexLine(recall); !strings.HasPrefix(line, "- @recall (v"+recall.Version()+"): ") {
		t.Fatalf("index line = %q", line)
	}
}

func TestSynthesizedDefsCarryTopLevelArguments(t *testing.T) {
	d, ok := synthesizePluginDef("recall", plugins.NewBuiltinRecallPlugin())
	if !ok {
		t.Fatal("recall must synthesize")
	}
	props, _ := d.Function.Parameters["properties"].(map[string]interface{})
	for _, k := range []string{"key", "offset", "max_chars", "match"} {
		if _, ok := props[k]; !ok {
			t.Fatalf("recall def lacks %q: %+v", k, d.Function.Parameters)
		}
	}
	if req, _ := d.Function.Parameters["required"].([]string); len(req) != 1 || req[0] != "key" {
		t.Fatalf("required = %v", d.Function.Parameters["required"])
	}
	if typ := props["offset"].(map[string]interface{})["type"]; typ != "integer" {
		t.Fatalf("offset type = %v", typ)
	}

	ask, ok := synthesizePluginDef("ask", plugins.NewBuiltinAskPlugin())
	if !ok || ask.Function.Parameters["type"] != "object" {
		t.Fatalf("@ask hands over its own JSON Schema: %+v", ask.Function.Parameters)
	}
	if _, ok := ask.Function.Parameters["properties"].(map[string]interface{})["questions"]; !ok {
		t.Fatalf("@ask schema must carry questions: %+v", ask.Function.Parameters)
	}

	moa, ok := synthesizePluginDef("moa", plugins.NewBuiltinMoaPlugin())
	if mp, _ := moa.Function.Parameters["properties"].(map[string]interface{}); !ok || mp["cmd"] == nil {
		t.Fatalf("a tool with subcommands keeps the cmd envelope: %+v", moa.Function.Parameters)
	}
}

func TestFlagsJSONSchemaTypes(t *testing.T) {
	if flagsJSONSchema(nil) != nil {
		t.Fatal("no flags, no schema")
	}
	s := flagsJSONSchema([]toolSchemaFlag{
		{Name: "a", Type: "int"}, {Name: "b", Type: "bool"}, {Name: "c", Type: "array"}, {Name: "d", Type: "weird"}, {Name: "e", Type: "number", Required: true},
	})
	props := s["properties"].(map[string]interface{})
	want := map[string]string{"a": "integer", "b": "boolean", "c": "array", "d": "string", "e": "number"}
	for k, typ := range want {
		if got := props[k].(map[string]interface{})["type"]; got != typ {
			t.Errorf("%s: type %v, want %s", k, got, typ)
		}
	}
	if props["c"].(map[string]interface{})["items"] == nil {
		t.Error("an array needs items")
	}
	if req := s["required"].([]string); len(req) != 1 || req[0] != "e" {
		t.Errorf("required = %v", req)
	}
}
