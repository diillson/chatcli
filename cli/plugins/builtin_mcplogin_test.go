/*
 * ChatCLI - @mcp-login tool tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeMCPAuthAdapter struct {
	loggedIn   string
	loggedOut  string
	statusText string
	loginErr   error
}

func (f *fakeMCPAuthAdapter) Login(_ context.Context, server string) (string, error) {
	if f.loginErr != nil {
		return "", f.loginErr
	}
	f.loggedIn = server
	return "authorized " + server, nil
}

func (f *fakeMCPAuthAdapter) Logout(server string) (string, error) {
	f.loggedOut = server
	return "forgot " + server, nil
}

func (f *fakeMCPAuthAdapter) Status() (string, error) {
	if f.statusText == "" {
		return "no servers", nil
	}
	return f.statusText, nil
}

func TestParseMCPLoginInvocation(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantCmd    string
		wantServer string
	}{
		{"empty→status", []string{}, "status", ""},
		{"envelope login", []string{`{"cmd":"login","args":{"server":"aws"}}`}, "login", "aws"},
		{"envelope status", []string{`{"cmd":"status"}`}, "status", ""},
		{"envelope logout", []string{`{"cmd":"logout","args":{"server":"gh"}}`}, "logout", "gh"},
		{"bare server object", []string{`{"server":"aws"}`}, "login", "aws"},
		{"top-level server field", []string{`{"cmd":"login","server":"aws"}`}, "login", "aws"},
		{"argv login", []string{"login", "aws"}, "login", "aws"},
		{"argv status", []string{"status"}, "status", ""},
		{"argv bare token", []string{"aws"}, "login", "aws"},
		{"alias auth", []string{"auth", "aws"}, "login", "aws"},
		{"alias forget", []string{"forget", "aws"}, "logout", "aws"},
		// Flattened flag forms — the agent rewrites {cmd,args} into --flag value.
		{"flag form logout", []string{"logout", "--server", "aws-mcp"}, "logout", "aws-mcp"},
		{"flag form login", []string{"login", "--server", "aws"}, "login", "aws"},
		{"flag eq form", []string{"logout", "--server=aws-mcp"}, "logout", "aws-mcp"},
		{"cmd+server flags", []string{"--cmd", "logout", "--server", "aws"}, "logout", "aws"},
		{"name alias flag", []string{"login", "--name", "aws"}, "login", "aws"},
		{"quoted server", []string{"login", `--server="aws-mcp"`}, "login", "aws-mcp"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd, server, err := parseMCPLoginInvocation(c.args)
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if cmd != c.wantCmd || server != c.wantServer {
				t.Fatalf("got (%q,%q), want (%q,%q)", cmd, server, c.wantCmd, c.wantServer)
			}
		})
	}
}

func TestMCPLoginExecuteDispatch(t *testing.T) {
	fake := &fakeMCPAuthAdapter{statusText: "aws: connected"}
	SetMCPAuthAdapter(fake)
	t.Cleanup(func() { SetMCPAuthAdapter(nil) })

	p := NewBuiltinMCPLoginPlugin()

	out, err := p.Execute(context.Background(), []string{`{"cmd":"login","args":{"server":"aws"}}`})
	if err != nil || !strings.Contains(out, "authorized aws") {
		t.Fatalf("login dispatch: out=%q err=%v", out, err)
	}
	if fake.loggedIn != "aws" {
		t.Fatalf("adapter.Login not called with aws, got %q", fake.loggedIn)
	}

	out, err = p.Execute(context.Background(), []string{`{"cmd":"status"}`})
	if err != nil || !strings.Contains(out, "connected") {
		t.Fatalf("status dispatch: out=%q err=%v", out, err)
	}

	out, err = p.Execute(context.Background(), []string{"logout", "aws"})
	if err != nil || !strings.Contains(out, "forgot aws") {
		t.Fatalf("logout dispatch: out=%q err=%v", out, err)
	}

	// login without server must error, not reach the adapter.
	if _, err = p.Execute(context.Background(), []string{`{"cmd":"login"}`}); err == nil {
		t.Fatalf("login without server should error")
	}
}

func TestMCPLoginExecuteErrors(t *testing.T) {
	// No adapter wired.
	SetMCPAuthAdapter(nil)
	p := NewBuiltinMCPLoginPlugin()
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"status"}`}); err == nil {
		t.Fatalf("expected error when no adapter is wired")
	}

	fake := &fakeMCPAuthAdapter{loginErr: errors.New("boom")}
	SetMCPAuthAdapter(fake)
	t.Cleanup(func() { SetMCPAuthAdapter(nil) })
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"login","args":{"server":"aws"}}`}); err == nil {
		t.Fatalf("expected login error to propagate")
	}
}

func TestMCPLoginShapeAndCaps(t *testing.T) {
	p := NewBuiltinMCPLoginPlugin()
	if p.Name() != "@mcp-login" {
		t.Fatalf("name=%q", p.Name())
	}
	if p.Path() != "" {
		t.Fatalf("builtin must have empty Path")
	}
	for _, s := range []string{p.Description(), p.Usage(), p.Version(), p.Schema()} {
		if strings.TrimSpace(s) == "" {
			t.Fatalf("empty metadata field")
		}
	}
	// Read-only gating: status yes, login/logout no.
	if !p.IsReadOnly([]string{`{"cmd":"status"}`}) {
		t.Errorf("status must be read-only")
	}
	if p.IsReadOnly([]string{`{"cmd":"login","args":{"server":"aws"}}`}) {
		t.Errorf("login must not be read-only")
	}
	if p.IsConcurrencySafe(nil) {
		t.Errorf("must not be concurrency-safe")
	}
	if strings.TrimSpace(p.DescribeCall([]string{`{"cmd":"login","args":{"server":"aws"}}`})) == "" {
		t.Errorf("DescribeCall empty")
	}
}
