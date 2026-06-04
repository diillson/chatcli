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
}

func (f *fakeGatewayAdapter) Name() string { return f.name }
func (f *fakeGatewayAdapter) Start(context.Context, chan<- gateway.InboundMessage) error {
	return nil
}
func (f *fakeGatewayAdapter) Send(_ context.Context, msg gateway.OutboundMessage) error {
	f.lastChat = msg.ChatID
	f.lastText = msg.Text
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
	if _, err := a.Send(context.Background(), fake.name, "ping"); err == nil {
		t.Fatal("expected error when no chat id and no home channel")
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
