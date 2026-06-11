/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package bedrock

import (
	"testing"

	"go.uber.org/zap"
)

func TestBuildCorporateHTTPClientUnset(t *testing.T) {
	t.Setenv("CHATCLI_BEDROCK_INSECURE_SKIP_VERIFY", "")
	t.Setenv("CHATCLI_BEDROCK_CA_BUNDLE", "")
	t.Setenv("CHATCLI_TLS_INSECURE_SKIP_VERIFY", "")
	t.Setenv("CHATCLI_CA_BUNDLE", "")
	client, note := buildCorporateHTTPClient(zap.NewNop())
	if client != nil || note != "" {
		t.Fatalf("expected SDK default client when no override is set, got %v / %q", client, note)
	}
}

func TestBuildCorporateHTTPClientGlobalInsecureFallback(t *testing.T) {
	t.Setenv("CHATCLI_BEDROCK_INSECURE_SKIP_VERIFY", "")
	t.Setenv("CHATCLI_BEDROCK_CA_BUNDLE", "")
	t.Setenv("CHATCLI_TLS_INSECURE_SKIP_VERIFY", "true")
	t.Setenv("CHATCLI_CA_BUNDLE", "")
	client, note := buildCorporateHTTPClient(zap.NewNop())
	if client == nil {
		t.Fatal("expected a custom client when the global insecure override is set")
	}
	if note == "" {
		t.Fatal("expected the insecure warning note")
	}
}

func TestBuildCorporateHTTPClientBedrockBundleKeepsPrecedence(t *testing.T) {
	// A Bedrock-specific bundle pointing to a missing file must fail open
	// (nil client), even when the global bundle var also points somewhere —
	// proving the Bedrock-specific var is the one being read.
	t.Setenv("CHATCLI_BEDROCK_INSECURE_SKIP_VERIFY", "")
	t.Setenv("CHATCLI_TLS_INSECURE_SKIP_VERIFY", "")
	t.Setenv("CHATCLI_BEDROCK_CA_BUNDLE", t.TempDir()+"/missing-bedrock.pem")
	t.Setenv("CHATCLI_CA_BUNDLE", t.TempDir()+"/missing-global.pem")
	client, _ := buildCorporateHTTPClient(zap.NewNop())
	if client != nil {
		t.Fatal("unreadable bundle must fall back to the SDK default client")
	}
}
