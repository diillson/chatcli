/*
 * ChatCLI - @voice plugin tests.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"context"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/gateway"
)

func newVoicePluginForTest() (*BuiltinVoicePlugin, *gateway.VoicePrefs) {
	prefs := gateway.NewVoicePrefs("") // memory-only
	return NewBuiltinVoicePlugin(prefs), prefs
}

func TestVoicePlugin_RefusesOutsideGateway(t *testing.T) {
	p, _ := newVoicePluginForTest()
	out, err := p.Execute(context.Background(), []string{`{"cmd":"on"}`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no active gateway conversation") {
		t.Fatalf("expected refusal outside gateway, got %q", out)
	}
}

func TestVoicePlugin_OnOffAutoRoundTrip(t *testing.T) {
	p, prefs := newVoicePluginForTest()
	prefs.SetActiveSession("telegram:42")

	if _, err := p.Execute(context.Background(), []string{`{"cmd":"on"}`}); err != nil {
		t.Fatal(err)
	}
	if got := prefs.Get("telegram:42"); got != gateway.VoicePrefAlways {
		t.Fatalf("after on: pref = %q, want always", got)
	}

	if _, err := p.Execute(context.Background(), []string{`{"cmd":"off"}`}); err != nil {
		t.Fatal(err)
	}
	if got := prefs.Get("telegram:42"); got != gateway.VoicePrefNever {
		t.Fatalf("after off: pref = %q, want never", got)
	}

	if _, err := p.Execute(context.Background(), []string{`{"cmd":"auto"}`}); err != nil {
		t.Fatal(err)
	}
	if got := prefs.Get("telegram:42"); got != "" {
		t.Fatalf("after auto: pref = %q, want empty", got)
	}
}

func TestVoicePlugin_StatusReportsSetting(t *testing.T) {
	p, prefs := newVoicePluginForTest()
	prefs.SetActiveSession("telegram:42")

	out, err := p.Execute(context.Background(), []string{`{"cmd":"status"}`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "default") {
		t.Fatalf("unset status must mention default, got %q", out)
	}

	if err := prefs.Set("telegram:42", gateway.VoicePrefAlways); err != nil {
		t.Fatal(err)
	}
	out, err = p.Execute(context.Background(), []string{"status"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "always") {
		t.Fatalf("always status must mention always, got %q", out)
	}
}

func TestParseVoiceCmd(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{`{"cmd":"on"}`, "on", true},
		{`{"cmd":"enable"}`, "on", true},
		{`{"cmd":"stop"}`, "off", true},
		{`{"cmd":"default"}`, "auto", true},
		{"on", "on", true},
		{"OFF", "off", true},
		{"", "status", true},
		{`{"cmd":"shout"}`, "", false},
		{"{bad json", "", false},
	}
	for _, tt := range tests {
		got, err := parseVoiceCmd([]string{tt.in})
		if tt.ok && (err != nil || got != tt.want) {
			t.Errorf("parseVoiceCmd(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
		if !tt.ok && err == nil {
			t.Errorf("parseVoiceCmd(%q) must fail", tt.in)
		}
	}
}
