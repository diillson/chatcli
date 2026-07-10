/*
 * ChatCLI - @mcp-login adapter + command tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/mcp"
)

func TestBuildMCPAuthNote(t *testing.T) {
	// No auth-required servers → empty note.
	if note := buildMCPAuthNote([]mcp.ServerStatus{{Name: "a", Connected: true}}); note != "" {
		t.Fatalf("expected empty note, got %q", note)
	}
	// One auth-required server → note names it and mentions the tool.
	note := buildMCPAuthNote([]mcp.ServerStatus{
		{Name: "aws", AuthRequired: true},
		{Name: "gh", Connected: true},
	})
	if !strings.Contains(note, "aws") {
		t.Fatalf("note should name the server: %q", note)
	}
}

func TestMCPLoginAdapterStatusAndLogout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cli := withConfiguredServers(t, "srv1", "srv2")
	a := &mcpLoginAdapter{cli: cli}

	out, err := a.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !strings.Contains(out, "srv1") || !strings.Contains(out, "srv2") {
		t.Fatalf("status missing servers: %q", out)
	}

	// Logout of a configured (not connected) server deletes nothing but
	// succeeds and returns a message.
	msg, err := a.Logout("srv1")
	if err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if strings.TrimSpace(msg) == "" {
		t.Fatalf("empty logout message")
	}

	// Login on a stdio server is rejected as non-remote (no browser opened).
	if _, err := a.Login(context.Background(), "srv1"); err == nil {
		t.Fatalf("expected non-remote login error for stdio server")
	}
}

func TestMCPLoginCommandUsagePaths(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cli := withConfiguredServers(t, "srv1")
	ctx := context.Background()

	// Empty name → usage branches (must not panic).
	cli.mcpLogin(ctx, "")
	cli.mcpLogout(ctx, "")

	// Real names: login errors (stdio non-remote), logout succeeds. Both just
	// print — we assert they run without panicking and touch the code paths.
	cli.mcpLogin(ctx, "srv1")
	cli.mcpLogout(ctx, "srv1")
}
