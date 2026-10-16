/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package client

import (
	"context"
	"sync"
	"testing"

	"github.com/diillson/chatcli/models"
)

// A keep-alive is bracketed by a send and a recv audit entry marked
// kind=keepalive; the recv carries the cache read, or the outcome of a
// request that did not complete.
func TestLogKeepAliveRequestAuditsBothEnds(t *testing.T) {
	var mu sync.Mutex
	var got []RequestAuditEvent
	RegisterRequestAuditorKeyed("keepalive-test", func(ev RequestAuditEvent) {
		mu.Lock()
		got = append(got, ev)
		mu.Unlock()
	})
	t.Cleanup(func() { RegisterRequestAuditorKeyed("keepalive-test", nil) })

	usage, err := LogKeepAliveRequest(context.Background(), nil, "CLAUDEAI", "m", 99, func() (*models.UsageInfo, error) {
		return &models.UsageInfo{CacheReadInputTokens: 7}, nil
	})
	if err != nil || usage == nil {
		t.Fatalf("LogKeepAliveRequest: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := LogKeepAliveRequest(ctx, nil, "CLAUDEAI", "m", 99, func() (*models.UsageInfo, error) {
		return nil, ctx.Err()
	}); err == nil {
		t.Fatal("the send error must come back")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 4 {
		t.Fatalf("want 4 audit entries, got %d: %+v", len(got), got)
	}
	for _, ev := range got {
		if ev.Fields["kind"] != KeepAliveRequestKind {
			t.Fatalf("entry not marked keepalive: %+v", ev)
		}
	}
	if got[0].Phase != "send" || got[0].Fields["payload_bytes"] != "99" {
		t.Fatalf("send entry: %+v", got[0])
	}
	if got[1].Phase != "recv" || got[1].Status != "success" || got[1].Fields["cache_read_tokens"] != "7" {
		t.Fatalf("recv entry: %+v", got[1])
	}
	if got[3].Status != "canceled" {
		t.Fatalf("cancelled keep-alive: %+v", got[3])
	}
}
