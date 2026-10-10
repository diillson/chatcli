/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/gateway"
)

// fakeGatewayAdapter is a minimal gateway.Adapter that records Send calls.
type fakeGatewayAdapter struct {
	name     string
	lastChat string
	lastText string
	sendErr  error
	lastKind string
	missing  string // returned by MissingOutbound when the fake implements it
}

// fakeReceiveOnlyAdapter is configured to receive but has nowhere to deliver,
// like the webhook without a callback URL.
type fakeReceiveOnlyAdapter struct{ fakeGatewayAdapter }

func (f *fakeReceiveOnlyAdapter) MissingOutbound() string { return f.missing }

func (f *fakeGatewayAdapter) Name() string { return f.name }
func (f *fakeGatewayAdapter) Start(context.Context, chan<- gateway.InboundMessage) error {
	return nil
}
func (f *fakeGatewayAdapter) Send(_ context.Context, msg gateway.OutboundMessage) error {
	f.lastChat = msg.ChatID
	f.lastText = msg.Text
	f.lastKind = msg.Kind
	return f.sendErr
}

func TestSplitTarget(t *testing.T) {
	cases := []struct {
		in       string
		platform string
		chatID   string
	}{
		{"telegram", "telegram", ""},
		{"Telegram", "telegram", ""},
		{"telegram:42", "telegram", "42"},
		{"telegram:-100123:7", "telegram", "-100123:7"},
		{"  whatsapp : +55119 ", "whatsapp", "+55119"},
	}
	for _, c := range cases {
		p, id := splitTarget(c.in)
		if p != c.platform || id != c.chatID {
			t.Errorf("splitTarget(%q) = (%q,%q), want (%q,%q)", c.in, p, id, c.platform, c.chatID)
		}
	}
}

func TestHomeChannelEnv(t *testing.T) {
	if got := homeChannelEnv("telegram"); got != "CHATCLI_TELEGRAM_HOME_CHANNEL" {
		t.Fatalf("homeChannelEnv = %q", got)
	}
}

func TestSendAdapter_ExplicitChatID(t *testing.T) {
	fake := &fakeGatewayAdapter{name: "sendtestexplicit"}
	gateway.RegisterBuilder(fake.name, func() (gateway.Adapter, error) { return fake, nil })

	a := &sendPluginAdapter{cli: nil}
	out, err := a.Send(context.Background(), fake.name+":chat99", "hello world")
	if err != nil {
		t.Fatalf("Send error: %v", err)
	}
	if fake.lastChat != "chat99" || fake.lastText != "hello world" {
		t.Fatalf("adapter got chat=%q text=%q", fake.lastChat, fake.lastText)
	}
	if !strings.Contains(out, "chat99") {
		t.Fatalf("result %q missing chat id", out)
	}
}

func TestSendAdapter_HomeChannel(t *testing.T) {
	fake := &fakeGatewayAdapter{name: "sendtesthome"}
	gateway.RegisterBuilder(fake.name, func() (gateway.Adapter, error) { return fake, nil })

	t.Setenv(homeChannelEnv(fake.name), "homechat")
	a := &sendPluginAdapter{cli: nil}
	if _, err := a.Send(context.Background(), fake.name, "ping"); err != nil {
		t.Fatalf("Send error: %v", err)
	}
	if fake.lastChat != "homechat" {
		t.Fatalf("expected home channel, got %q", fake.lastChat)
	}
}

func TestSendAdapter_NoHomeChannel(t *testing.T) {
	fake := &fakeGatewayAdapter{name: "sendtestnohome"}
	gateway.RegisterBuilder(fake.name, func() (gateway.Adapter, error) { return fake, nil })

	a := &sendPluginAdapter{cli: nil}
	_, err := a.Send(context.Background(), fake.name, "ping")
	if err == nil {
		t.Fatal("expected error when no chat id and no home channel")
	}
	// The hint names both ways out, with every placeholder filled.
	if msg := err.Error(); strings.Contains(msg, "%!") ||
		!strings.Contains(msg, fake.name+":chat_id") || !strings.Contains(msg, homeChannelEnv(fake.name)) {
		t.Fatalf("hint is malformed: %q", msg)
	}
}

func TestSendAdapter_ProactiveKind(t *testing.T) {
	fake := &fakeGatewayAdapter{name: "sendtestkind"}
	gateway.RegisterBuilder(fake.name, func() (gateway.Adapter, error) { return fake, nil })

	a := &sendPluginAdapter{cli: nil}
	if _, err := a.Send(context.Background(), fake.name+":c", "x"); err != nil {
		t.Fatal(err)
	}
	if fake.lastKind != gateway.OutboundProactive {
		t.Fatalf("kind = %q, want %q", fake.lastKind, gateway.OutboundProactive)
	}
}

// A platform that can receive but not deliver must fail the send, not report
// a message that went nowhere.
func TestSendAdapter_NowhereToDeliver(t *testing.T) {
	fake := &fakeReceiveOnlyAdapter{fakeGatewayAdapter{name: "sendtestrecvonly", missing: "CHATCLI_FAKE_CALLBACK"}}
	gateway.RegisterBuilder(fake.name, func() (gateway.Adapter, error) { return fake, nil })

	a := &sendPluginAdapter{cli: nil}
	_, err := a.Send(context.Background(), fake.name+":c", "x")
	if err == nil || !strings.Contains(err.Error(), "CHATCLI_FAKE_CALLBACK") {
		t.Fatalf("expected a nowhere-to-deliver error naming the setting, got %v", err)
	}
	if fake.lastText != "" {
		t.Fatal("nothing must be handed to the adapter")
	}

	fake.missing = ""
	if _, err := a.Send(context.Background(), fake.name+":c", "x"); err != nil {
		t.Fatalf("a wired platform must send: %v", err)
	}
}

func TestSendAdapter_NotConfigured(t *testing.T) {
	a := &sendPluginAdapter{cli: nil}
	if _, err := a.Send(context.Background(), "definitelynotaplatform", "x"); err == nil {
		t.Fatal("expected error for unconfigured platform")
	}
}

func TestSendAdapter_SendError(t *testing.T) {
	fake := &fakeGatewayAdapter{name: "sendtesterr", sendErr: errors.New("api down")}
	gateway.RegisterBuilder(fake.name, func() (gateway.Adapter, error) { return fake, nil })

	a := &sendPluginAdapter{cli: nil}
	_, err := a.Send(context.Background(), fake.name+":c1", "x")
	if err == nil || !strings.Contains(err.Error(), "api down") {
		t.Fatalf("expected wrapped api error, got %v", err)
	}
}

func TestSendAdapter_List(t *testing.T) {
	fake := &fakeGatewayAdapter{name: "sendtestlist"}
	gateway.RegisterBuilder(fake.name, func() (gateway.Adapter, error) { return fake, nil })
	t.Setenv(homeChannelEnv(fake.name), "lc")

	a := &sendPluginAdapter{cli: nil}
	out, err := a.List(context.Background())
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if !strings.Contains(out, fake.name) || !strings.Contains(out, "lc") {
		t.Fatalf("list output missing entry: %q", out)
	}
}
