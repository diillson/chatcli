/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package utils

import "testing"

func TestScrubSecretEnv_DropsChatCLISecretsKeepsTheRest(t *testing.T) {
	env := []string{"PATH=/usr/bin", "CHATCLI_ENCRYPTION_KEY=k", "CHATCLI_ENCRYPTION_KEY_PREVIOUS=old",
		"CHATCLI_JWT_SECRET=j", "CHATCLI_AUTH_DIR=/a", "CHATCLI_AUDIT_LOG_PATH=/l", "CHATCLI_MANAGED_CONFIG=/m", "OPENAI_API_KEY=x", "HOME=/h"}
	got := ScrubSecretEnv(env, nil)
	want := []string{"PATH=/usr/bin", "OPENAI_API_KEY=x", "HOME=/h"}
	if len(got) != len(want) {
		t.Fatalf("scrubbed = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("scrubbed[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	// Explicit passthrough keeps a denylisted key.
	got = ScrubSecretEnv(env, map[string]string{"CHATCLI_AUDIT_LOG_PATH": "${CHATCLI_AUDIT_LOG_PATH}"})
	if len(got) != 4 || got[3] != "HOME=/h" {
		t.Fatalf("passthrough must keep the allowed key: %v", got)
	}
	found := false
	for _, kv := range got {
		if kv == "CHATCLI_AUDIT_LOG_PATH=/l" {
			found = true
		}
	}
	if !found {
		t.Fatal("allowed key must survive")
	}
	if !IsSecretEnvKey("CHATCLI_ENCRYPTION_KEY_2") || IsSecretEnvKey("CHATCLI_ENCRYPTION") || IsSecretEnvKey("CHATCLI_MODEL") {
		t.Fatal("prefix matching")
	}
}
