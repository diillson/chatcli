/*
 * ChatCLI - Tests for conversation-bound reasoning blocks
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package client

import (
	"errors"
	"fmt"
	"testing"

	"github.com/diillson/chatcli/models"
)

// The rejection text of the Fable 5.2 migration guide (platform.claude.com/
// docs/en/models/fable-5-2/migration-guide).
const bindingRejection = "messages.5.content.0: Invalid `signature` in `thinking` block. The block is bound to a different conversation. Remove the block, or set `thinking.block_binding.prefix_mismatch_behavior` to \"drop_block\"."

func thinkingHistory() []models.Message {
	return []models.Message{
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: "a1", Thinking: []models.ThinkingBlock{
			{Type: "thinking", Thinking: "t1", Signature: "sig-1"},
			{Type: "redacted_thinking", Data: "blob-1"},
		}},
		{Role: "user", Content: "q2"},
	}
}

func countThinking(history []models.Message) int {
	n := 0
	for _, m := range history {
		n += len(m.Thinking)
	}
	return n
}

func TestIsThinkingBindingError(t *testing.T) {
	if !IsThinkingBindingError(fmt.Errorf("bedrock: ValidationException: %s", bindingRejection)) {
		t.Fatal("a wrapped rejection must be recognized")
	}
	if IsThinkingBindingError(nil) || IsThinkingBindingError(errors.New("400 invalid signature in thinking block")) {
		t.Fatal("only the conversation-binding rejection qualifies")
	}
}

func TestStaleThinkingHeal_DropsBlocksAndResendsOnce(t *testing.T) {
	var s StaleThinking
	history := thinkingHistory()
	var seen []int
	var out string
	dropped, err := s.Heal(history, func(h []models.Message) error {
		seen = append(seen, countThinking(h))
		if countThinking(h) > 0 {
			return errors.New(bindingRejection)
		}
		out = "ok"
		return nil
	})
	if err != nil || out != "ok" || !dropped {
		t.Fatalf("out=%q dropped=%v err=%v", out, dropped, err)
	}
	if len(seen) != 2 || seen[0] != 2 || seen[1] != 0 {
		t.Fatalf("want one send with the blocks and one without, got %v", seen)
	}
	if countThinking(history) != 2 {
		t.Fatal("the caller's history must not be modified")
	}

	// Later requests leave the refused blocks out from the first send, and
	// keep the blocks produced after the refusal.
	history = append(history, models.Message{Role: "assistant", Content: "a2", Thinking: []models.ThinkingBlock{
		{Type: "thinking", Thinking: "t2", Signature: "sig-2"},
	}})
	sends := 0
	dropped, err = s.Heal(history, func(h []models.Message) error {
		sends++
		if countThinking(h) != 1 || h[3].Thinking[0].Signature != "sig-2" {
			t.Fatalf("only the new block may travel, got %d", countThinking(h))
		}
		return nil
	})
	if err != nil || dropped || sends != 1 {
		t.Fatalf("dropped=%v sends=%d err=%v", dropped, sends, err)
	}
}

func TestStaleThinkingHeal_NoLoopAndOtherErrorsUntouched(t *testing.T) {
	var s StaleThinking
	sends := 0
	dropped, err := s.Heal([]models.Message{{Role: "user", Content: "q"}}, func([]models.Message) error {
		sends++
		return errors.New(bindingRejection)
	})
	if sends != 1 || dropped || !IsThinkingBindingError(err) {
		t.Fatalf("nothing to drop means no resend: sends=%d dropped=%v err=%v", sends, dropped, err)
	}

	sends = 0
	boom := errors.New("529 overloaded")
	dropped, err = s.Heal(thinkingHistory(), func([]models.Message) error {
		sends++
		return boom
	})

	// A client built without the state sends once and never resends.
	var none *StaleThinking
	if d, e := none.Heal(thinkingHistory(), func([]models.Message) error { return errors.New(bindingRejection) }); d || !IsThinkingBindingError(e) {
		t.Fatalf("nil state: dropped=%v err=%v", d, e)
	}
	if sends != 1 || dropped || !errors.Is(err, boom) {
		t.Fatalf("another error is returned as is: sends=%d dropped=%v err=%v", sends, dropped, err)
	}
}
