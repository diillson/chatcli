/*
 * ChatCLI - Mode transition cleanup tests.
 */
package cli

import (
	"testing"

	"github.com/diillson/chatcli/models"
	"github.com/stretchr/testify/assert"
)

func TestModeOfSystemMessage_FlatContent(t *testing.T) {
	cases := []struct {
		name    string
		msg     models.Message
		wantMod string
	}{
		{
			name:    "chat marker",
			msg:     models.Message{Role: "system", Content: ChatModeSystemHint},
			wantMod: ModeChat,
		},
		{
			name:    "coder marker",
			msg:     models.Message{Role: "system", Content: CoderSystemPrompt},
			wantMod: ModeCoder,
		},
		{
			name:    "agent marker via FormatInstructions wrapper",
			msg:     models.Message{Role: "system", Content: "[ACTIVE MODE: /agent]\nfoo"},
			wantMod: ModeAgent,
		},
		{
			name:    "non-system role returns empty",
			msg:     models.Message{Role: "user", Content: "[ACTIVE MODE: chat]"},
			wantMod: "",
		},
		{
			name:    "system without marker returns empty",
			msg:     models.Message{Role: "system", Content: "just some context"},
			wantMod: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.wantMod, modeOfSystemMessage(tc.msg))
		})
	}
}

// TestModeOfSystemMessage_StructuredParts covers the Anthropic-style
// path where the flat Content is empty but the marker lives inside
// SystemParts. If the fallback misses, agent <-> chat transitions
// would silently leak the wrong prompt to providers that consume
// SystemParts (Anthropic) while purgeStaleModeSystems thinks the
// message is mode-neutral.
func TestModeOfSystemMessage_StructuredParts(t *testing.T) {
	msg := models.Message{
		Role:    "system",
		Content: "",
		SystemParts: []models.ContentBlock{
			{Type: "text", Text: "[ACTIVE MODE: /coder]\nbody"},
		},
	}
	assert.Equal(t, ModeCoder, modeOfSystemMessage(msg))
}

// TestPurgeStaleModeSystems_DropsOtherModes is the headline behavior
// test: the cross-mode-leak bug the user reported (chat → coder →
// chat shipping BOTH prompts to the LLM) is fixed by this filter.
// Asserting the wire shape after purge proves the regression cannot
// silently come back.
func TestPurgeStaleModeSystems_DropsOtherModes(t *testing.T) {
	history := []models.Message{
		{Role: "system", Content: "[ACTIVE MODE: /coder]\nstale coder prompt"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello"},
		{Role: "system", Content: "[ACTIVE MODE: /agent]\nalso stale"},
	}
	got := purgeStaleModeSystems(history, ModeChat)

	// Both stale system messages are gone. Conversational history is
	// preserved verbatim — we don't want to erase the user/assistant
	// thread just because the system slot changed mode.
	assert.Len(t, got, 2)
	assert.Equal(t, "user", got[0].Role)
	assert.Equal(t, "assistant", got[1].Role)
}

// TestPurgeStaleModeSystems_KeepsCurrentMode covers the converse —
// the system message of the current mode survives the purge so
// in-place updates (replace, not prepend) still work in agent_mode.go.
func TestPurgeStaleModeSystems_KeepsCurrentMode(t *testing.T) {
	history := []models.Message{
		{Role: "system", Content: "[ACTIVE MODE: /coder]\ncurrent coder prompt"},
		{Role: "user", Content: "task"},
	}
	got := purgeStaleModeSystems(history, ModeCoder)
	assert.Len(t, got, 2)
	assert.Equal(t, "system", got[0].Role)
	assert.Contains(t, got[0].Content, "[ACTIVE MODE: /coder]")
}

// TestPurgeStaleModeSystems_KeepsMarkerlessSystems is the most
// subtle case: a system message WITHOUT a mode marker (e.g.,
// /context attach injection) must survive every transition. We
// can't tell the user "your attached context got nuked because you
// switched modes" — that would defeat the feature.
func TestPurgeStaleModeSystems_KeepsMarkerlessSystems(t *testing.T) {
	history := []models.Message{
		{Role: "system", Content: "[ACTIVE MODE: /coder]\nstale"},
		{Role: "system", Content: "Context attached by the user — keep."},
		{Role: "user", Content: "hi"},
	}
	got := purgeStaleModeSystems(history, ModeChat)
	assert.Len(t, got, 2)
	// The markerless system survives.
	assert.Equal(t, "system", got[0].Role)
	assert.Contains(t, got[0].Content, "Context attached")
}
