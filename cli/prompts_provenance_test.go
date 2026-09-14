package cli

import (
	"strings"
	"testing"
)

// Every mode prompt binds specific claims to tool evidence and labels
// the model's own knowledge where it is used. The rule is a fixed line
// in the stable prompt, never a per-query switch, so the cached prefix
// stays byte-stable.
func TestModePromptsBindClaimsToEvidence(t *testing.T) {
	for name, prompt := range map[string]string{
		"CoderSystemPrompt":       CoderSystemPrompt,
		"CoderFormatInstructions": CoderFormatInstructions,
		"AgentFormatInstructions": AgentFormatInstructions,
	} {
		if !strings.Contains(prompt, "Provenance") {
			t.Errorf("%s must carry the provenance rule", name)
		}
		for _, want := range []string{"unverified", "estimate", "evidence"} {
			if !strings.Contains(prompt, want) {
				t.Errorf("%s provenance rule must mention %q", name, want)
			}
		}
	}
}
