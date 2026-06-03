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

func TestDescribeCall_ContextualLabels(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string // substring expected (value, since i18n returns key+args in tests)
	}{
		{"image gen", NewBuiltinImagePlugin().DescribeCall([]string{"gen", "--prompt", "a watercolor fox"}), "a watercolor fox"},
		{"image status", NewBuiltinImagePlugin().DescribeCall([]string{"status"}), "status"},
		{"speak say", NewBuiltinSpeakPlugin().DescribeCall([]string{"say", "--text", "olá mundo"}), "olá mundo"},
		{"session search", NewBuiltinSessionPlugin().DescribeCall([]string{"search", "--query", "rate limiter"}), "rate limiter"},
		{"session list", NewBuiltinSessionPlugin().DescribeCall([]string{"list"}), "list"},
		{"osv check", NewBuiltinOsvPlugin().DescribeCall([]string{"check", "--package", "requests", "--version", "2.19.0"}), "requests"},
		{"osv scan", NewBuiltinOsvPlugin().DescribeCall([]string{"scan", "--path", "go.mod"}), "go.mod"},
		{"skill create", NewBuiltinSkillPlugin().DescribeCall([]string{"create", "--name", "deploy-x"}), "deploy-x"},
		{"skill stats", NewBuiltinSkillPlugin().DescribeCall([]string{"stats"}), "stats"},
		{"send send", NewBuiltinSendPlugin().DescribeCall([]string{"send", "--to", "telegram", "--message", "hi"}), "telegram"},
		{"send list", NewBuiltinSendPlugin().DescribeCall([]string{"list"}), "list"},
		{"moa ask", NewBuiltinMoaPlugin().DescribeCall([]string{"ask", "--prompt", "why"}), "moa"},
		{"moa list", NewBuiltinMoaPlugin().DescribeCall([]string{"list"}), "list"},
	}
	for _, c := range cases {
		if strings.TrimSpace(c.got) == "" {
			t.Errorf("%s: DescribeCall returned empty (would fall back to long Description)", c.name)
			continue
		}
		if !strings.Contains(strings.ToLower(c.got), strings.ToLower(c.want)) {
			t.Errorf("%s: %q does not contain %q", c.name, c.got, c.want)
		}
	}
}

func TestDescribeCall_DefaultsOnBadArgs(t *testing.T) {
	// Empty/garbled args must still return a concise non-empty label, never "".
	for _, got := range []string{
		NewBuiltinImagePlugin().DescribeCall(nil),
		NewBuiltinSpeakPlugin().DescribeCall(nil),
		NewBuiltinSkillPlugin().DescribeCall(nil),
		NewBuiltinOsvPlugin().DescribeCall(nil),
		NewBuiltinMoaPlugin().DescribeCall(nil),
		NewBuiltinSendPlugin().DescribeCall(nil),
		NewBuiltinSessionPlugin().DescribeCall(nil),
	} {
		if strings.TrimSpace(got) == "" {
			t.Error("DescribeCall(nil) returned empty")
		}
	}
}
