/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/pkg/pulse/pulsetest"
	"go.uber.org/zap"
)

// One inbound message is one node named by its platform; the agent run, the
// voice synthesis and the reply delivery hang from it, and nothing that
// identifies the conversation or its content reaches the bus.
func TestInboundMessageIsOneNodeWithItsWorkNestedUnderIt(t *testing.T) {
	rec := pulsetest.Record(t)
	adapter := &recordingAdapter{}
	var agentParent string
	agent := func(ctx context.Context, _ string, _ string) (string, error) {
		agentParent = pulse.ParentFrom(ctx)
		return "SECRET-REPLY", nil
	}
	r := NewRunner([]Adapter{adapter}, agent, zap.NewNop(), 1)
	r.thinkingDelay = 1 << 40
	setEchoSynthesizer(r)

	r.handle(context.Background(), InboundMessage{
		Platform: "rec", ChatID: "SECRET-CHAT", UserID: "SECRET-USER", Text: "SECRET-TEXT",
		Audio: &InboundAudio{Data: []byte("voice-note"), MimeType: "audio/ogg"},
	})

	start := rec.WaitFor(t, "inbound start", pulsetest.Is(pulse.KindRPC, "gateway:rec", pulse.PhaseStart))
	end := rec.WaitFor(t, "inbound end", pulsetest.Is(pulse.KindRPC, "gateway:rec", pulse.PhaseEnd))
	if agentParent != start.ID {
		t.Fatalf("agent parent = %q, want the inbound node %q", agentParent, start.ID)
	}
	if end.Status != pulse.StatusOK || end.Attrs["platform"] != "rec" || end.Attrs["media"] != "voice" ||
		end.Attrs["reply_kind"] != "voice" || end.Attrs["direction"] != "inbound" {
		t.Fatalf("inbound end = %+v", end)
	}
	voice := rec.WaitFor(t, "voice end", pulsetest.Is(pulse.KindBackground, "gateway:voice", pulse.PhaseEnd))
	if voice.Parent != start.ID || voice.Status != pulse.StatusOK {
		t.Fatalf("voice = %+v", voice)
	}
	send := rec.WaitFor(t, "send end", pulsetest.Is(pulse.KindBackground, "gateway:send", pulse.PhaseEnd))
	if send.Parent != start.ID || send.Attrs["kind"] != OutboundFinal || send.Status != pulse.StatusOK {
		t.Fatalf("send = %+v", send)
	}
	wire, _ := json.Marshal(rec.Events())
	if strings.Contains(string(wire), "SECRET") {
		t.Fatalf("message, chat or user leaked onto the bus: %s", wire)
	}
}

func TestInboundNodeFailsWhenTheAgentFails(t *testing.T) {
	rec := pulsetest.Record(t)
	adapter := &recordingAdapter{}
	agent := func(context.Context, string, string) (string, error) { return "", errors.New("SECRET-ERR") }
	r := NewRunner([]Adapter{adapter}, agent, zap.NewNop(), 1)
	r.thinkingDelay = 1 << 40

	r.handle(context.Background(), InboundMessage{Platform: "rec", ChatID: "1", Text: "hi",
		Document: &InboundDocument{Data: []byte("%PDF"), MimeType: "application/pdf"}})

	end := rec.WaitFor(t, "inbound end", pulsetest.Is(pulse.KindRPC, "gateway:rec", pulse.PhaseEnd))
	if end.Status != pulse.StatusError || end.Attrs["media"] != "pdf" || end.Attrs["reply_kind"] != "text" {
		t.Fatalf("inbound end = %+v", end)
	}
	wire, _ := json.Marshal(rec.Events())
	if strings.Contains(string(wire), "SECRET") {
		t.Fatalf("error text leaked onto the bus: %s", wire)
	}
}

func TestPulseInboundMediaAndReplyKind(t *testing.T) {
	for want, msg := range map[string]InboundMessage{
		"text":     {},
		"voice":    {Audio: &InboundAudio{}},
		"image":    {Image: &InboundImage{}},
		"pdf":      {Document: &InboundDocument{}},
		"document": {Document: &InboundDocument{MimeType: "application/msword"}},
	} {
		if got := pulseInboundMedia(&msg); got != want {
			t.Errorf("media = %q, want %q", got, want)
		}
	}
	for want, out := range map[string]OutboundMessage{
		"text":        {},
		"voice":       {Audio: &OutboundAudio{}},
		"image":       {Image: &OutboundImage{}},
		"voice+image": {Audio: &OutboundAudio{}, Image: &OutboundImage{}},
	} {
		if got := pulseReplyKind(&out); got != want {
			t.Errorf("reply kind = %q, want %q", got, want)
		}
	}
}

// A platform request hangs from the delivery whose context it carries, and an
// adapter's client meters its requests even before the daemon injects a
// logger.
func TestPlatformClientMetersBeforeSetLoggerAndNestsUnderTheDelivery(t *testing.T) {
	rec := pulsetest.Record(t)
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	c := newPlatformClient(0, "telegram")
	ctx := pulse.WithParent(context.Background(), "background-77")
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/bot1:SECRET/sendMessage", nil)
	resp, err := c.Do(req) // #nosec G704 -- local test server
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	ev := rec.WaitFor(t, "conn end", pulsetest.Is(pulse.KindConn, "127.0.0.1", pulse.PhaseEnd))
	if ev.Parent != "background-77" || ev.Attrs["platform"] != "telegram" {
		t.Fatalf("conn = %+v", ev)
	}
	// SetLogger replaces the dashboard-only transport instead of stacking a
	// second one over it, which would report every request twice.
	logged := newLoggingClient(c, zap.NewNop(), "telegram")
	lt, ok := logged.Transport.(*loggingTransport)
	if !ok || lt.logger == nil {
		t.Fatalf("transport = %T", logged.Transport)
	}
	if _, stacked := lt.base.(*loggingTransport); stacked {
		t.Fatal("logging transport stacked over the dashboard-only one")
	}
}
