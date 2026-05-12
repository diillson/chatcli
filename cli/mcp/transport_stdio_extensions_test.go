/*
 * ChatCLI - Tests for stdio transport behavior added by config extensions
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Exercises the cwd validation path on newStdioTransport without
 * actually spawning a process. We pick a config that resolves to a
 * non-existent directory and assert on the error shape so the
 * eventual operator log is actionable.
 */
package mcp

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestNewStdioTransport_CwdMustExist(t *testing.T) {
	cfg := ServerConfig{
		Name:    "fs",
		Command: "/usr/bin/true",
		Cwd:     "/this/path/should/not/exist/c7d1f9",
	}
	_, err := newStdioTransport(context.Background(), cfg, zap.NewNop())
	if err == nil {
		t.Fatal("expected error for non-existent cwd")
	}
	if !strings.Contains(err.Error(), "MCP cwd") {
		t.Errorf("error message should call out the cwd field; got %q", err)
	}
}

func TestNewStdioTransport_CwdMustBeDirectory(t *testing.T) {
	// Point cwd at a real file (this very test source) so the Stat
	// succeeds but IsDir returns false — that's the second guard.
	file := filepath.Join(".", "transport_stdio_extensions_test.go")
	cfg := ServerConfig{
		Name:    "fs",
		Command: "/usr/bin/true",
		Cwd:     file,
	}
	_, err := newStdioTransport(context.Background(), cfg, zap.NewNop())
	if err == nil {
		t.Fatal("expected error for cwd pointing at a regular file")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("error should explain the kind of failure; got %q", err)
	}
}

func TestNewStdioTransport_EmptyCwdInheritsParent(t *testing.T) {
	// Empty Cwd must NOT trigger the cwd validation path at all —
	// the child inherits the parent's working directory and there's
	// nothing to validate.
	cfg := ServerConfig{
		Name:    "fs",
		Command: "/usr/bin/true",
		Cwd:     "",
	}
	tp, err := newStdioTransport(context.Background(), cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("empty cwd should not error: %v", err)
	}
	// Clean up the launched process so the test stays hermetic.
	defer func() { _ = tp.Close() }()

	if tp.cmd.Dir != "" {
		t.Errorf("cmd.Dir should remain unset when Cwd is empty; got %q", tp.cmd.Dir)
	}
}

func TestNewStdioTransport_CustomTimeoutPropagates(t *testing.T) {
	cfg := ServerConfig{
		Name:    "fs",
		Command: "/usr/bin/true",
		Timeout: 7,
	}
	tp, err := newStdioTransport(context.Background(), cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("transport setup: %v", err)
	}
	defer func() { _ = tp.Close() }()

	if tp.callTimeout.Seconds() != 7 {
		t.Errorf("callTimeout = %s, want 7s", tp.callTimeout)
	}
}

func TestNewStdioTransport_DefaultTimeoutPropagates(t *testing.T) {
	cfg := ServerConfig{
		Name:    "fs",
		Command: "/usr/bin/true",
	}
	tp, err := newStdioTransport(context.Background(), cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("transport setup: %v", err)
	}
	defer func() { _ = tp.Close() }()

	if tp.callTimeout != DefaultRequestTimeout {
		t.Errorf("callTimeout = %s, want %s", tp.callTimeout, DefaultRequestTimeout)
	}
}
