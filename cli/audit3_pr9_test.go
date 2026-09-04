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

	"github.com/diillson/chatcli/models"
)

// overflowFakeClient fails with a context-overflow error until failures
// are exhausted, then answers.
type overflowFakeClient struct {
	rpcChatFakeClient
	failures int
	calls    int
	sizes    []int
}

func (f *overflowFakeClient) SendPrompt(ctx context.Context, p string, hist []models.Message, n int) (string, error) {
	if !strings.HasPrefix(p, "hi") {
		// The compactor's summarizer call rides the same fake client.
		return "summary", nil
	}
	f.calls++
	chars := 0
	for _, m := range hist {
		chars += len(m.Content)
	}
	f.sizes = append(f.sizes, chars)
	if f.calls <= f.failures {
		return "", errors.New("This model's maximum context length is 8192 tokens. However, your messages resulted in 9000 tokens.")
	}
	return f.rpcChatFakeClient.SendPrompt(ctx, p, hist, n)
}

func bulkyHistory(n int) []models.Message {
	var h []models.Message
	for i := 0; i < n; i++ {
		h = append(h, models.Message{Role: "user", Content: strings.Repeat("q", 3000)}, models.Message{Role: "assistant", Content: strings.Repeat("a", 6000)})
	}
	return h
}

func TestRunChatTurnRPC_RecoversFromContextOverflow(t *testing.T) {
	fake := &overflowFakeClient{rpcChatFakeClient: rpcChatFakeClient{reply: "recovered"}, failures: 2}
	c := newRPCChatCLI(t, &fake.rpcChatFakeClient)
	c.Client = fake
	turn, err := c.RunChatTurnRPC(context.Background(), "sess-ovf", "hi", bulkyHistory(30), RPCChatOpts{})
	if err != nil {
		t.Fatalf("the RPC turn must recover: %v", err)
	}
	if turn.Reply != "recovered" || fake.calls != 3 {
		t.Fatalf("reply=%q calls=%d", turn.Reply, fake.calls)
	}
	if fake.sizes[2] >= fake.sizes[0] {
		t.Fatalf("each retry must carry a smaller history: %v", fake.sizes)
	}
}

func TestRunChatTurnRPC_OverflowRecoveryIsBounded(t *testing.T) {
	fake := &overflowFakeClient{failures: 100}
	c := newRPCChatCLI(t, &fake.rpcChatFakeClient)
	c.Client = fake
	_, err := c.RunChatTurnRPC(context.Background(), "sess-ovf2", "hi", bulkyHistory(30), RPCChatOpts{})
	if err == nil {
		t.Fatal("a provider that always overflows must fail the turn")
	}
	// one send + refresh-on-auth check (no) + MaxRecoveryAttempts retries
	if fake.calls != 1+3 {
		t.Fatalf("calls = %d, want the bounded 4", fake.calls)
	}
}

func TestRecoverOverflow_IgnoresOtherErrors(t *testing.T) {
	c := newRPCChatCLI(t, &rpcChatFakeClient{})
	c.history = bulkyHistory(3)
	before := len(c.history)
	rec := c.newOverflowRecovery("chat", nil)
	if c.recoverOverflow(context.Background(), rec, errors.New("401 unauthorized")) || len(c.history) != before {
		t.Fatal("non-overflow errors must not touch the history")
	}
	if c.recoverOverflow(context.Background(), rec, context.Canceled) {
		t.Fatal("cancellation is never recovered")
	}
	// A standalone thread (MoA participant) compacts without touching cli.history.
	thread := bulkyHistory(20)
	out, ok := rec.recoverHistory(errors.New("prompt is too long: 9 tokens > 8 maximum"), thread)
	if !ok || len(c.history) != before {
		t.Fatalf("standalone recovery: ok=%v history=%d", ok, len(c.history))
	}
	total := 0
	for _, m := range out {
		total += len(m.Content)
	}
	if total >= 20*9000 {
		t.Fatalf("standalone thread must shrink: %d", total)
	}
}
