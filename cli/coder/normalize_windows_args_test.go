/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package coder

import (
	"testing"
)

// Regression for the Windows field failure: the policy normalizer must still
// extract the subcommand and flags when the args envelope is double-encoded
// as a JSON string and/or carries unescaped Windows-path backslashes —
// otherwise the security prompt degrades to a raw JSON blob and rules like
// "@coder write" never match.
func TestNormalizeCoderArgs_WindowsPaths(t *testing.T) {
	tests := []struct {
		name     string
		args     string
		wantSub  string
		wantNorm string
	}{
		{
			name:     "string envelope with invalid escapes",
			args:     `{"args":"{\"file\":\"C:\\Users\\builder\\deployment.yaml\",\"content\":\"QQ==\",\"encoding\":\"base64\"}","cmd":"write"}`,
			wantSub:  "write",
			wantNorm: `write --file C:\Users\builder\deployment.yaml`,
		},
		{
			name:     "top-level invalid escapes",
			args:     `{"cmd":"write","args":{"file":"C:\Users\builder\deployment.yaml","content":"QQ==","encoding":"base64"}}`,
			wantSub:  "write",
			wantNorm: `write --file C:\Users\builder\deployment.yaml`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sub, norm := NormalizeCoderArgs(tt.args)
			if sub != tt.wantSub {
				t.Errorf("subcommand = %q, want %q", sub, tt.wantSub)
			}
			if norm != tt.wantNorm {
				t.Errorf("normalized = %q, want %q", norm, tt.wantNorm)
			}
		})
	}
}
