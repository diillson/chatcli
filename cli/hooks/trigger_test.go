/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package hooks

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestCommandHook_ReceivesTrigger(t *testing.T) {
	m := NewManager(zap.NewNop())
	res := m.executeCommandHook(context.Background(), HookConfig{Name: "t", Event: EventPreCompact, Type: HookTypeCommand, Command: "echo trigger=$CHATCLI_HOOK_TRIGGER; cat"},
		HookEvent{Type: EventPreCompact, Trigger: "recovery", Timestamp: time.Now()}, 5*time.Second)
	if res == nil || !strings.Contains(res.Output, "trigger=recovery") || !strings.Contains(res.Output, `"trigger":"recovery"`) {
		t.Fatalf("trigger must reach env and JSON: %+v", res)
	}
}
