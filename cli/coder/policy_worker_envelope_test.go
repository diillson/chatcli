package coder

import (
	"testing"

	"go.uber.org/zap"
)

// Workers canonicalize native tool calls to the "@coder" +
// {"cmd":subcmd,"args":{...}} envelope before the policy check (see
// workers.policyCallSurface). These tests pin the contract: rules written
// against "@coder <subcmd>" must match that surface.
func TestCheck_WorkerCanonicalEnvelopeMatchesRules(t *testing.T) {
	pm := &PolicyManager{
		Rules: []Rule{
			{Pattern: "@coder read", Action: ActionAllow},
			{Pattern: "@coder exec", Action: ActionAsk},
			{Pattern: "@coder write --file /etc", Action: ActionDeny},
		},
		configPath: t.TempDir() + "/policy.json",
		logger:     zap.NewNop(),
	}

	if got := pm.Check("@coder", `{"cmd":"read","args":{"file":"main.go"}}`); got != ActionAllow {
		t.Errorf("read envelope = %v, want allow", got)
	}
	if got := pm.Check("@coder", `{"cmd":"exec","args":{"cmd":"go build ./..."}}`); got != ActionAsk {
		t.Errorf("exec envelope = %v, want ask (explicit rule)", got)
	}
	if got := pm.Check("@coder", `{"cmd":"write","args":{"file":"/etc/passwd","content":"x"}}`); got != ActionDeny {
		t.Errorf("write /etc envelope = %v, want deny", got)
	}
}

// The pre-fix surface — toolName "run_command" with the shell command in
// args["cmd"] — never matched "@coder ..." rules; the worker adapter no
// longer produces it, but the mismatch is pinned here as documentation of
// why the canonicalization exists.
func TestCheck_LegacyNativeSurfaceNeverMatchedCoderRules(t *testing.T) {
	pm := &PolicyManager{
		Rules:      []Rule{{Pattern: "@coder exec", Action: ActionAllow}},
		configPath: t.TempDir() + "/policy.json",
		logger:     zap.NewNop(),
	}
	if got := pm.Check("run_command", `{"cmd":"go build ./..."}`); got != ActionAsk {
		t.Errorf("legacy surface = %v, want ask (rule cannot match)", got)
	}
}
