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

// fakeChannelsAdapter scripts the ChannelsAdapter contract for tests.
type fakeChannelsAdapter struct {
	listOut     string
	unreadOut   string
	ackNotify   int
	ackUnread   int
	lastChannel string
	lastLimit   int
	acked       bool
}

func (f *fakeChannelsAdapter) ListMessages(channel string, limit int) (string, error) {
	f.lastChannel, f.lastLimit = channel, limit
	return f.listOut, nil
}
func (f *fakeChannelsAdapter) UnreadSummary() (string, error) { return f.unreadOut, nil }
func (f *fakeChannelsAdapter) Ack() (int, int, error) {
	f.acked = true
	return f.ackNotify, f.ackUnread, nil
}

func withChannelsAdapter(a ChannelsAdapter) func() {
	SetChannelsAdapter(a)
	return func() { SetChannelsAdapter(nil) }
}

func TestChannelsRequiresAdapter(t *testing.T) {
	SetChannelsAdapter(nil)
	p := NewBuiltinChannelsPlugin()
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"list"}`}); err == nil {
		t.Fatal("expected error when MCP is not wired")
	}
}

func TestChannelsList_EnvelopeAndFlattenedArgs(t *testing.T) {
	fa := &fakeChannelsAdapter{listOut: "[seq 3] srv/alerts at 10:00:00: disk full"}
	defer withChannelsAdapter(fa)()
	p := NewBuiltinChannelsPlugin()

	out, err := p.Execute(context.Background(), []string{`{"cmd":"list","args":{"channel":"alerts","limit":5}}`})
	if err != nil || !strings.Contains(out, "disk full") {
		t.Fatalf("envelope list failed: out=%q err=%v", out, err)
	}
	if fa.lastChannel != "alerts" || fa.lastLimit != 5 {
		t.Errorf("args not forwarded: channel=%q limit=%d", fa.lastChannel, fa.lastLimit)
	}

	// Flattened + bare-word forms models also produce.
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"list","channel":"ci","limit":3}`}); err != nil {
		t.Errorf("flattened form must parse: %v", err)
	}
	if fa.lastChannel != "ci" || fa.lastLimit != 3 {
		t.Errorf("flattened args not forwarded: channel=%q limit=%d", fa.lastChannel, fa.lastLimit)
	}
	if _, err := p.Execute(context.Background(), []string{"list", "incidents"}); err != nil {
		t.Errorf("bare-word form must parse: %v", err)
	}
	if fa.lastChannel != "incidents" {
		t.Errorf("bare-word channel not forwarded: %q", fa.lastChannel)
	}
}

func TestChannelsList_EmptyInboxFriendlyMessage(t *testing.T) {
	defer withChannelsAdapter(&fakeChannelsAdapter{})()
	p := NewBuiltinChannelsPlugin()
	out, err := p.Execute(context.Background(), []string{`{"cmd":"list"}`})
	if err != nil || !strings.Contains(out, "no matching messages") {
		t.Errorf("empty inbox must return a friendly line, got out=%q err=%v", out, err)
	}
}

func TestChannelsUnreadAndAck(t *testing.T) {
	fa := &fakeChannelsAdapter{unreadOut: "1 new alert", ackNotify: 2, ackUnread: 7}
	defer withChannelsAdapter(fa)()
	p := NewBuiltinChannelsPlugin()

	out, err := p.Execute(context.Background(), []string{`{"cmd":"unread"}`})
	if err != nil || out != "1 new alert" {
		t.Fatalf("unread failed: out=%q err=%v", out, err)
	}

	out, err = p.Execute(context.Background(), []string{`{"cmd":"ack"}`})
	if err != nil || !fa.acked {
		t.Fatalf("ack must reach the adapter: out=%q err=%v", out, err)
	}
	if !strings.Contains(out, "7 unread") || !strings.Contains(out, "2 pending") {
		t.Errorf("ack summary must report cleared counts, got %q", out)
	}
}

func TestChannelsAliasesAndCaps(t *testing.T) {
	fa := &fakeChannelsAdapter{unreadOut: "x"}
	defer withChannelsAdapter(fa)()
	p := NewBuiltinChannelsPlugin()

	if _, err := p.Execute(context.Background(), []string{`{"cmd":"new"}`}); err != nil {
		t.Errorf("alias 'new' must fold to unread: %v", err)
	}
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"bogus"}`}); err == nil {
		t.Error("unknown subcommand must error")
	}

	if !p.IsReadOnly([]string{`{"cmd":"list"}`}) || !p.IsReadOnly([]string{`{"cmd":"unread"}`}) {
		t.Error("list/unread must advertise read-only")
	}
	if p.IsReadOnly([]string{`{"cmd":"ack"}`}) {
		t.Error("ack mutates inbox state and must not advertise read-only")
	}
	if !p.IsConcurrencySafe(nil) {
		t.Error("@channels is lock-protected and concurrency-safe")
	}
}
