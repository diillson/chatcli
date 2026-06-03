/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"context"
	"strings"
	"testing"
)

// fakeSendAdapter records calls and returns canned results.
type fakeSendAdapter struct {
	lastTarget  string
	lastMessage string
	sendErr     error
	listOut     string
}

func (f *fakeSendAdapter) Send(_ context.Context, target, message string) (string, error) {
	f.lastTarget = target
	f.lastMessage = message
	if f.sendErr != nil {
		return "", f.sendErr
	}
	return "sent:" + target, nil
}

func (f *fakeSendAdapter) List(_ context.Context) (string, error) {
	return f.listOut, nil
}

func withSendAdapter(t *testing.T, a SendAdapter) {
	t.Helper()
	SetSendAdapter(a)
	t.Cleanup(func() { SetSendAdapter(nil) })
}

func TestSend_NoAdapter(t *testing.T) {
	SetSendAdapter(nil)
	p := NewBuiltinSendPlugin()
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"list"}`}); err == nil {
		t.Fatal("expected error when no adapter is wired")
	}
}

func TestSend_EnvelopeSend(t *testing.T) {
	f := &fakeSendAdapter{}
	withSendAdapter(t, f)
	p := NewBuiltinSendPlugin()

	out, err := p.Execute(context.Background(), []string{`{"cmd":"send","args":{"to":"telegram:42","message":"hi there"}}`})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.lastTarget != "telegram:42" || f.lastMessage != "hi there" {
		t.Fatalf("adapter got target=%q message=%q", f.lastTarget, f.lastMessage)
	}
	if !strings.Contains(out, "telegram:42") {
		t.Fatalf("result %q missing target", out)
	}
}

func TestSend_FlatJSONSend(t *testing.T) {
	f := &fakeSendAdapter{}
	withSendAdapter(t, f)
	p := NewBuiltinSendPlugin()

	// cmd + sibling fields, no nested "args".
	_, err := p.Execute(context.Background(), []string{`{"cmd":"send","to":"slack","message":"deploy done"}`})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.lastTarget != "slack" || f.lastMessage != "deploy done" {
		t.Fatalf("adapter got target=%q message=%q", f.lastTarget, f.lastMessage)
	}
}

func TestSend_ArgvSend(t *testing.T) {
	f := &fakeSendAdapter{}
	withSendAdapter(t, f)
	p := NewBuiltinSendPlugin()

	_, err := p.Execute(context.Background(), []string{"send", "--to", "discord", "--message", "ping"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.lastTarget != "discord" || f.lastMessage != "ping" {
		t.Fatalf("adapter got target=%q message=%q", f.lastTarget, f.lastMessage)
	}
}

func TestSend_List(t *testing.T) {
	f := &fakeSendAdapter{listOut: "telegram\nslack"}
	withSendAdapter(t, f)
	p := NewBuiltinSendPlugin()

	out, err := p.Execute(context.Background(), []string{`{"cmd":"list"}`})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "telegram\nslack" {
		t.Fatalf("unexpected list output %q", out)
	}
}

func TestSend_MissingFields(t *testing.T) {
	f := &fakeSendAdapter{}
	withSendAdapter(t, f)
	p := NewBuiltinSendPlugin()

	cases := []string{
		`{"cmd":"send","args":{"message":"no target"}}`,
		`{"cmd":"send","args":{"to":"telegram"}}`,
	}
	for _, c := range cases {
		if _, err := p.Execute(context.Background(), []string{c}); err == nil {
			t.Fatalf("expected validation error for %s", c)
		}
	}
}

func TestSend_UnknownCmd(t *testing.T) {
	withSendAdapter(t, &fakeSendAdapter{})
	p := NewBuiltinSendPlugin()
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"explode"}`}); err == nil {
		t.Fatal("expected error for unknown cmd")
	}
}

func TestSend_EmptyArgs(t *testing.T) {
	withSendAdapter(t, &fakeSendAdapter{})
	p := NewBuiltinSendPlugin()
	if _, err := p.Execute(context.Background(), nil); err == nil {
		t.Fatal("expected error for empty args")
	}
}

func TestCanonicalSendCmd(t *testing.T) {
	for _, in := range []string{"send", "SEND", " message ", "notify", "deliver", "msg"} {
		if got := canonicalSendCmd(in); got != "send" {
			t.Errorf("canonicalSendCmd(%q) = %q, want send", in, got)
		}
	}
	for _, in := range []string{"list", "targets", "platforms", "channels"} {
		if got := canonicalSendCmd(in); got != "list" {
			t.Errorf("canonicalSendCmd(%q) = %q, want list", in, got)
		}
	}
	if got := canonicalSendCmd("nope"); got != "" {
		t.Errorf("canonicalSendCmd(nope) = %q, want empty", got)
	}
}

func TestSchemaParses(t *testing.T) {
	p := NewBuiltinSendPlugin()
	if !strings.Contains(p.Schema(), "subcommands") {
		t.Fatal("schema missing subcommands")
	}
	if p.Name() != "@send" {
		t.Fatalf("name = %q", p.Name())
	}
}
