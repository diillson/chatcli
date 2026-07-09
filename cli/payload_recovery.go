/*
 * ChatCLI - Payload Recovery Helpers
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Small helpers for the agent-mode payload/WAF rejection recovery path in
 * agent_mode.go. Kept in their own file so the dedup contract is unit-
 * testable without dragging the full agent loop into tests.
 */
package cli

import (
	"strings"

	"github.com/diillson/chatcli/models"
)

// payloadRecoveryHintMarker prefixes the one-shot steering hint injected
// after a proxy/gateway payload rejection. The marker doubles as the dedup
// key: recovery must never stack a second copy — every extra hint grows the
// very payload the middlebox demands we shrink.
const payloadRecoveryHintMarker = "[SYSTEM NOTICE — PAYLOAD LIMIT HIT]"

// historyContainsPayloadHint reports whether the payload-recovery hint is
// already present anywhere in the history (compaction may have moved or
// summarized it into another message, so match by content, not position).
func historyContainsPayloadHint(history []models.Message) bool {
	for _, msg := range history {
		if strings.Contains(msg.Content, payloadRecoveryHintMarker) {
			return true
		}
	}
	return false
}
