/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package mail

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestSendAndDrain(t *testing.T) {
	r := NewRegistry(10)
	msg, err := r.Send("reviewer", "coder", "card-1", "tests missing in foo_test.go")
	if err != nil {
		t.Fatal(err)
	}
	if msg.ID != "msg-1" || msg.To != "coder" {
		t.Fatalf("unexpected message: %+v", msg)
	}

	got := r.Drain("coder")
	if len(got) != 1 || got[0].Text != "tests missing in foo_test.go" {
		t.Fatalf("drain mismatch: %+v", got)
	}
	if again := r.Drain("coder"); again != nil {
		t.Fatalf("second drain must be empty, got %+v", again)
	}
}

func TestRecipientNormalization(t *testing.T) {
	r := NewRegistry(10)
	if _, err := r.Send("a", "  CoDeR ", "", "x"); err != nil {
		t.Fatal(err)
	}
	if got := r.Drain("coder"); len(got) != 1 {
		t.Fatalf("normalized recipient not matched: %+v", got)
	}
}

func TestValidation(t *testing.T) {
	r := NewRegistry(10)
	if _, err := r.Send("", "coder", "", "x"); err == nil {
		t.Fatal("empty sender must fail")
	}
	if _, err := r.Send("a", " ", "", "x"); err == nil {
		t.Fatal("empty recipient must fail")
	}
	if _, err := r.Send("a", "b", "", "  "); err == nil {
		t.Fatal("empty text must fail")
	}
}

func TestPeekDoesNotRemove(t *testing.T) {
	r := NewRegistry(10)
	_, _ = r.Send("a", "coder", "", "one")
	if got := r.Peek("coder"); len(got) != 1 {
		t.Fatalf("peek failed: %+v", got)
	}
	if got := r.Drain("coder"); len(got) != 1 {
		t.Fatal("peek must not remove")
	}
}

func TestPendingCounts(t *testing.T) {
	r := NewRegistry(10)
	_, _ = r.Send("a", "coder", "", "1")
	_, _ = r.Send("a", "coder", "", "2")
	_, _ = r.Send("a", "reviewer", "", "3")
	p := r.Pending()
	if p["coder"] != 2 || p["reviewer"] != 1 {
		t.Fatalf("pending mismatch: %+v", p)
	}
}

func TestQueueCapDropsOldest(t *testing.T) {
	r := NewRegistry(500)
	for i := 0; i < maxQueuePerRecipient+5; i++ {
		if _, err := r.Send("a", "coder", "", fmt.Sprintf("m%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	got := r.Drain("coder")
	if len(got) != maxQueuePerRecipient {
		t.Fatalf("queue cap not enforced: %d", len(got))
	}
	if got[0].Text != "m5" {
		t.Fatalf("oldest not dropped first: %s", got[0].Text)
	}
}

func TestHistoryRingAndRecent(t *testing.T) {
	r := NewRegistry(3)
	for i := 0; i < 5; i++ {
		_, _ = r.Send("a", "b", "", fmt.Sprintf("m%d", i))
	}
	recent := r.Recent(0)
	if len(recent) != 3 || recent[0].Text != "m4" || recent[2].Text != "m2" {
		t.Fatalf("history ring wrong: %+v", recent)
	}
}

func TestFormatInbox(t *testing.T) {
	if FormatInbox(nil) != "" {
		t.Fatal("empty inbox must render empty")
	}
	out := FormatInbox([]Message{
		{From: "reviewer", CardID: "card-2", Text: "fix the tests"},
		{From: "user", Text: "prioritize the login flow"},
	})
	for _, want := range []string{"[SQUAD MAIL]", "from=reviewer", "card=card-2", "fix the tests", "from=user"} {
		if !strings.Contains(out, want) {
			t.Fatalf("FormatInbox missing %q in:\n%s", want, out)
		}
	}
}

func TestNilSafety(t *testing.T) {
	var r *Registry
	if _, err := r.Send("a", "b", "", "x"); err == nil {
		t.Fatal("nil registry Send must error")
	}
	if r.Drain("x") != nil || r.Peek("x") != nil || r.Recent(1) != nil || r.Pending() != nil {
		t.Fatal("nil registry reads must be nil")
	}
}

func TestConcurrentSendDrain(t *testing.T) {
	r := NewRegistry(1000)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				_, _ = r.Send("s", fmt.Sprintf("agent-%d", n%4), "", "m")
			}
		}(i)
	}
	wg.Wait()
	total := 0
	for i := 0; i < 4; i++ {
		total += len(r.Drain(fmt.Sprintf("agent-%d", i)))
	}
	if total != 160 {
		t.Fatalf("expected 160 delivered, got %d", total)
	}
}
