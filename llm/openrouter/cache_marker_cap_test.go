/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package openrouter

import "testing"

func TestApplyOpenRouterCacheControlMarker_CapsAtFourKeepingTheLatest(t *testing.T) {
	var messages []map[string]interface{}
	for i := 0; i < 6; i++ {
		messages = append(messages, map[string]interface{}{"role": "system", "content": "part"})
	}
	messages = append(messages, map[string]interface{}{"role": "user", "content": "hi"})
	applyOpenRouterCacheControlMarker(messages, map[string]string{"type": "ephemeral"})
	count := 0
	marked := func(msg map[string]interface{}) bool {
		parts, _ := msg["content"].([]map[string]interface{})
		for _, p := range parts {
			if _, ok := p["cache_control"]; ok {
				return true
			}
		}
		return false
	}
	for _, m := range messages {
		if marked(m) {
			count++
		}
	}
	if count != anthropicMaxCacheMarkers {
		t.Fatalf("markers = %d, want the upstream cap %d", count, anthropicMaxCacheMarkers)
	}
	if !marked(messages[6]) || marked(messages[0]) || marked(messages[1]) || !marked(messages[5]) {
		t.Fatal("the rolling user breakpoint and the latest system markers must survive; the earliest go")
	}
}
