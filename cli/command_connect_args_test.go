/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/i18n"
)

// /connect dropped what it did not understand: a mistyped --tls connected in
// clear text and a --token with no value connected without credentials.
func TestConnectArgsProblem(t *testing.T) {
	ok := [][]string{
		{"/connect", "host:50051"},
		{"/connect", "host:50051", "--token", "abc", "--tls"},
		{"/connect", "host:50051", "--provider", "openai", "--model", "gpt-x", "--use-local-auth"},
		{"/connect", "host:50051", "--ca-cert", "/tmp/ca.pem", "--client-id", "id", "--client-key", "key"},
	}
	for _, args := range ok {
		if p := connectArgsProblem(args); p != "" {
			t.Errorf("%v reported a problem: %s", args, p)
		}
	}
	bad := map[string][]string{
		i18n.T("connect.error.unknown_arg", "--tsl"):        {"/connect", "host:50051", "--tsl"},
		i18n.T("connect.error.unknown_arg", "extra"):        {"/connect", "host:50051", "extra"},
		i18n.T("connect.error.missing_value", "--token"):    {"/connect", "host:50051", "--token"},
		i18n.T("connect.error.missing_value", "--provider"): {"/connect", "host:50051", "--provider", "--tls"},
		i18n.T("connect.error.address_missing"):             {"/connect", "--tls"},
	}
	for want, args := range bad {
		if got := connectArgsProblem(args); got != want {
			t.Errorf("%v: problem = %q, want %q", args, got, want)
		}
	}
	// The parser still fills every field for a line with no problem.
	p := parseConnectArgs([]string{"/connect", "h:1", "--token", "t", "--model", "m", "--tls", "--use-local-auth"})
	if p.token != "t" || p.model != "m" || !p.useTLS || !p.useLocalAuth {
		t.Fatalf("parsed: %+v", p)
	}
}

// The handler refuses the line before touching the network.
func TestConnectRefusesAMistypedFlag(t *testing.T) {
	ch := NewCommandHandler(&ChatCLI{})
	out := captureStdout(t, func() { ch.handleConnectCommand(t.Context(), "/connect host:50051 --tsl") })
	if !strings.Contains(out, "--tsl") || ch.cli.isRemote {
		t.Fatalf("a mistyped flag was not refused:\n%s", out)
	}
}
