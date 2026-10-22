/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"strings"
	"testing"
)

// capsCase is one invocation with the answers the orchestrator must get.
type capsCase struct {
	args     []string
	readOnly bool
	parallel bool
}

func checkCaps(t *testing.T, name string, p Plugin, cases []capsCase) {
	t.Helper()
	for _, c := range cases {
		if got := IsReadOnly(p, c.args); got != c.readOnly {
			t.Errorf("%s IsReadOnly(%v) = %v, want %v", name, c.args, got, c.readOnly)
		}
		if got := IsConcurrencySafe(p, c.args); got != c.parallel {
			t.Errorf("%s IsConcurrencySafe(%v) = %v, want %v", name, c.args, got, c.parallel)
		}
	}
}

func TestReadOnlyCaps_PureReadsSkipTheGate(t *testing.T) {
	checkCaps(t, "@osv", NewBuiltinOsvPlugin(), []capsCase{
		{nil, true, true},
		{[]string{`{"cmd":"scan","args":{"path":"."}}`}, true, true},
		{[]string{"check", "--ecosystem", "Go", "--package", "x", "--version", "1"}, true, true},
		{[]string{`{not json`}, false, false},
	})
	checkCaps(t, "@json", NewBuiltinJSONPlugin(), []capsCase{
		{[]string{`{"cmd":"get","args":{"file":"a.json","path":"x"}}`}, true, true},
		{[]string{"schema", "a.json"}, true, true},
		{[]string{`{"file":"a.json","where":"x=1"}`}, true, true},
		{[]string{`{"cmd":"explode"}`}, false, false},
		{[]string{`{not json`}, false, false},
	})
	checkCaps(t, "@commands", NewBuiltinCommandsPlugin(), []capsCase{
		{[]string{`{"cmd":"list"}`}, true, true},
		{[]string{"get", "review-pr", "12"}, true, true},
	})
	checkCaps(t, "@image", NewBuiltinImagePlugin(), []capsCase{
		{[]string{`{"cmd":"status"}`}, true, true},
		{[]string{"models"}, true, true},
		{[]string{`{"cmd":"use","args":{"model":"x"}}`}, false, false},
		{[]string{`{"cmd":"gen","args":{"prompt":"a fox"}}`}, false, false},
		{[]string{`{"cmd":"edit","args":{"prompt":"bluer","file":"a.png"}}`}, false, false},
	})
	checkCaps(t, "@speak", NewBuiltinSpeakPlugin(), []capsCase{
		{[]string{`{"cmd":"status"}`}, true, false},
		{[]string{`{"cmd":"say","args":{"text":"hi"}}`}, false, false},
	})
	checkCaps(t, "@send", NewBuiltinSendPlugin(), []capsCase{
		{[]string{`{"cmd":"list"}`}, true, true},
		{[]string{`{"cmd":"send","args":{"to":"x","text":"hi"}}`}, false, false},
	})
	checkCaps(t, "@moa", NewBuiltinMoaPlugin(), []capsCase{
		{[]string{`{"cmd":"list"}`}, true, true},
		{[]string{`{"cmd":"ask","args":{"prompt":"x"}}`}, false, false},
	})
	checkCaps(t, "@skill", NewBuiltinSkillPlugin(), []capsCase{
		{[]string{`{"cmd":"list"}`}, true, true},
		{[]string{`{"cmd":"show","args":{"name":"x"}}`}, true, true},
		{[]string{"stats"}, true, true},
		{[]string{`{"cmd":"create","args":{"name":"x","description":"d","content":"c"}}`}, false, false},
		{[]string{`{"cmd":"remove","args":{"name":"x"}}`}, false, false},
		{[]string{`{"cmd":"restore","args":{"name":"x"}}`}, false, false},
		{[]string{`{"cmd":"export"}`}, false, false},
	})
	checkCaps(t, "@voice", NewBuiltinVoicePlugin(nil), []capsCase{
		{[]string{`{"cmd":"status"}`}, true, true},
		{[]string{"on"}, false, false},
		{[]string{`{"cmd":"auto"}`}, false, false},
	})
	checkCaps(t, "@compress", NewBuiltinCompressPlugin(), []capsCase{
		{[]string{`{"cmd":"stats"}`}, true, true},
		{[]string{`{"content":"x"}`}, true, true},
	})
	checkCaps(t, "@park", NewBuiltinParkPlugin(), []capsCase{
		{[]string{`{"cmd":"list"}`}, true, false},
		{[]string{"list"}, true, false},
		{[]string{`{"cmd":"delay","args":{"duration":"5m"}}`}, false, false},
	})
	checkCaps(t, "@scheduler", NewBuiltinSchedulerPlugin(), []capsCase{
		{[]string{`{"cmd":"get","args":{"id":"j1"}}`}, true, true}, // alias of query
		{[]string{"list"}, true, true},
		{[]string{`{"cmd":"schedule","args":{"name":"n","at":"2027-01-01T00:00:00Z"}}`}, false, false},
	})
	checkCaps(t, "@http", NewBuiltinHTTPPlugin(), []capsCase{
		{[]string{`{"cmd":"get","args":{"url":"http://x"}}`}, true, true},
		{[]string{`{"cmd":"post","args":{"url":"http://x","body":"{}"}}`}, false, false},
	})
	checkCaps(t, "@embed", NewBuiltinEmbedPlugin(), []capsCase{
		{[]string{`{"cmd":"text","args":{"text":"a","out":"/tmp/v.jsonl"}}`}, false, false},
		{[]string{`{"cmd":"status"}`}, true, true},
	})
}

// TestDescribeCall_NewBuiltins pins the spinner label of every verb that
// had none. i18n is not initialized under `go test`, so T() returns the
// raw key plus the formatted argument — the assertions name the key
// fragment and the echoed argument, stable regardless of locale loading.
func TestDescribeCall_NewBuiltins(t *testing.T) {
	cases := []struct {
		name string
		p    Plugin
		args []string
		want string
	}{
		{"@commands list", NewBuiltinCommandsPlugin(), []string{`{"cmd":"list"}`}, "commands.describe.list"},
		{"@commands get", NewBuiltinCommandsPlugin(), []string{`{"cmd":"get","args":{"name":"review-pr"}}`}, "review-pr"},
		{"@voice status", NewBuiltinVoicePlugin(nil), []string{"status"}, "voice.describe.status"},
		{"@voice on", NewBuiltinVoicePlugin(nil), []string{`{"cmd":"on"}`}, "voice.describe.on"},
		{"@compress", NewBuiltinCompressPlugin(), []string{`{"content":"x"}`}, "compress.describe.compress"},
		{"@compress stats", NewBuiltinCompressPlugin(), []string{`{"cmd":"stats"}`}, "compress.describe.stats"},
		{"@recall key", NewBuiltinRecallPlugin(), []string{`{"key":"CCR_ABC123"}`}, "CCR_ABC123"},
		{"@recall bare", NewBuiltinRecallPlugin(), []string{"CCR_XYZ"}, "CCR_XYZ"},
		{"@memory remember", NewBuiltinMemoryPlugin(), []string{`{"cmd":"remember","args":{"content":"User likes Go"}}`}, "User likes Go"},
		{"@memory recall", NewBuiltinMemoryPlugin(), []string{`{"cmd":"recall","args":{"query":"certs"}}`}, "certs"},
		{"@memory map", NewBuiltinMemoryPlugin(), []string{"map"}, "memory.describe.map"},
		{"@session get", NewBuiltinSessionPlugin(), []string{`{"cmd":"get","args":{"id":"s-42"}}`}, "s-42"},
		{"@session fork", NewBuiltinSessionPlugin(), []string{`{"cmd":"fork"}`}, "session.describe.fork"},
		{"@session attach", NewBuiltinSessionPlugin(), []string{`{"cmd":"attach","args":{"id":"s-42"}}`}, "session.describe.attach"},
		{"@skill restore", NewBuiltinSkillPlugin(), []string{`{"cmd":"restore","args":{"name":"deploy-x"}}`}, "skill.describe.restore"},
		{"@scheduler alias get", NewBuiltinSchedulerPlugin(), []string{`{"cmd":"get","args":{"id":"j1"}}`}, "j1"},
	}
	for _, c := range cases {
		d, ok := c.p.(DescriberWithInput)
		if !ok {
			t.Fatalf("%s: no DescribeCall", c.name)
		}
		got := d.DescribeCall(c.args)
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: DescribeCall = %q, want it to contain %q", c.name, got, c.want)
		}
	}
}

func TestSchemaSurfaces_RestoreViewChannels(t *testing.T) {
	skill := NewBuiltinSkillPlugin()
	if !strings.Contains(skill.Usage(), "restore {name}") || !strings.Contains(skill.Schema(), `"restore"`) {
		t.Fatal("@skill restore must be documented in Usage and Schema")
	}
	if !strings.Contains(NewBuiltinViewPlugin().Schema(), `"name": "file"`) {
		t.Fatal("@view must declare its file argument")
	}
	if ch := NewBuiltinChannelsPlugin().Schema(); !strings.Contains(ch, `"channel"`) || !strings.Contains(ch, `"limit"`) {
		t.Fatal("@channels list must declare channel and limit flags")
	}
}
