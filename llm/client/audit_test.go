/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package client

import (
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestRequestAuditor_ReceivesSendAndRecv(t *testing.T) {
	var got []RequestAuditEvent
	RegisterRequestAuditor(func(ev RequestAuditEvent) { got = append(got, ev) })
	t.Cleanup(func() { RegisterRequestAuditor(nil) })

	LogRequestStart(zap.NewNop(), "OPENAI", "gpt-5.6",
		zap.Int("payload_bytes", 1234), zap.Int("history_len", 7), zap.Bool("stream", true),
		zap.Duration("timeout", 3*time.Second), zap.Float64("temperature", 0.5), zap.Any("body", map[string]int{"x": 1}))
	LogRequestFinish(nil, "OPENAI", "gpt-5.6", "success", 250*time.Millisecond, zap.String("stop", "end"))

	if len(got) != 2 {
		t.Fatalf("expected send+recv, got %d", len(got))
	}
	send := got[0]
	if send.Phase != "send" || send.Provider != "OPENAI" || send.Model != "gpt-5.6" {
		t.Fatalf("send = %+v", send)
	}
	if send.Fields["payload_bytes"] != "1234" || send.Fields["history_len"] != "7" || send.Fields["stream"] != "true" ||
		send.Fields["timeout"] != "3s" || send.Fields["temperature"] != "0.5" {
		t.Fatalf("fields = %v", send.Fields)
	}
	if _, leaked := send.Fields["body"]; leaked {
		t.Fatal("non-scalar fields must never reach the audit trail")
	}
	recv := got[1]
	if recv.Phase != "recv" || recv.Status != "success" || recv.Duration != 250*time.Millisecond || recv.Fields["stop"] != "end" {
		t.Fatalf("recv = %+v", recv)
	}

	// Cleared sink: no panic, nothing recorded.
	RegisterRequestAuditor(nil)
	LogRequestStart(zap.NewNop(), "XAI", "grok-4.6")
	if len(got) != 2 {
		t.Fatal("events recorded after the sink was cleared")
	}
}
