/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package gateway

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

const fakeBotToken = "123456789:AAFakeTokenForTestsOnly_xyz" // #nosec G101 -- synthetic test value

// closedAddr returns a loopback address nothing listens on, so a dial fails.
func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// The Bot API puts the token in the URL path; a dial error must not carry
// it, neither in the returned error nor in anything logged.
func TestTelegramDialErrorDoesNotLeakToken(t *testing.T) {
	core, logs := observer.New(zap.DebugLevel)
	logger := zap.New(core)
	a := NewTelegramAdapter(fakeBotToken, []string{"1"}, logger)
	a.SetLogger(logger)
	a.baseURL = "http://" + closedAddr(t)

	errs := []error{}
	_, _, err := a.poll(context.Background())
	errs = append(errs, err)
	errs = append(errs, a.Send(context.Background(), OutboundMessage{ChatID: "1", Text: "x"}))
	errs = append(errs, a.SendTyping(context.Background(), "1"))
	_, _, err = a.downloadFile(context.Background(), "FILE", 1024)
	errs = append(errs, err)
	for i, err := range errs {
		if err == nil {
			t.Fatalf("call %d: expected a dial error", i)
		}
		if strings.Contains(err.Error(), fakeBotToken) || strings.Contains(err.Error(), "AAFakeToken") {
			t.Errorf("call %d: error leaks the token: %v", i, err)
		}
		if !strings.Contains(err.Error(), "/bot***/") {
			t.Errorf("call %d: error should keep the masked path for diagnosis: %v", i, err)
		}
	}

	// The poll loop logs its failure: that line must be clean too.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_ = a.Start(ctx, make(chan InboundMessage))
	if logs.Len() == 0 {
		t.Fatal("expected the poll failure to be logged")
	}
	for _, e := range logs.All() {
		line := e.Message
		for _, f := range e.Context {
			line += " " + f.String
			if f.Interface != nil {
				if er, ok := f.Interface.(error); ok {
					line += " " + er.Error()
				}
			}
		}
		if strings.Contains(line, "AAFakeToken") {
			t.Errorf("log line leaks the token: %s", line)
		}
	}
}

func TestRedactURL(t *testing.T) {
	cases := map[string]string{
		"https://api.telegram.org/bot123:ABC/getUpdates?offset=1":          "https://api.telegram.org/bot***/getUpdates",
		"https://api.telegram.org/file/bot123:ABC/voice/file_1.oga":        "https://api.telegram.org/file/bot***/voice/file_1.oga",
		"https://cdn.discordapp.com/a/1/x.png?ex=1&is=2&hm=signature":      "https://cdn.discordapp.com/a/1/x.png",
		"https://user:pass@hooks.example.com/cb?token=s3cret#frag":         "https://hooks.example.com/cb",
		"https://lookaside.fbsbx.com/whatsapp_business/attachments/?mid=1": "https://lookaside.fbsbx.com/whatsapp_business/attachments/",
	}
	for in, want := range cases {
		if got := redactURL(in); got != want {
			t.Errorf("redactURL(%q) = %q, want %q", in, got, want)
		}
	}
}
