/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package mediapulse

import "testing"

func TestProviderLabelNeverCarriesAPath(t *testing.T) {
	for in, want := range map[string]string{
		"embedded:kokoro/af_heart":               "embedded",
		"command:/usr/local/bin/piper":           "command",
		"local:whisper-cli/base":                 "local",
		"openai:whisper-1":                       "openai",
		"openai-responses":                       "openai-responses",
		"/opt/secret/bin":                        "unknown",
		`C:\Users\someone\tool.exe`:              "c",
		"":                                       "unknown",
		"Google":                                 "google",
		"weird name with spaces":                 "weird",
		"base.en":                                "base.en",
		"averyveryveryveryveryverylonglabelname": "averyveryveryveryveryverylonglab",
	} {
		if got := ProviderLabel(in); got != want {
			t.Errorf("ProviderLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEngine(t *testing.T) {
	for label, want := range map[string]string{
		"embedded": "local", "local": "local", "command": "local",
		"openai": "remote", "selfhosted": "remote", "bedrock": "remote",
	} {
		if got := Engine(label); got != want {
			t.Errorf("Engine(%q) = %q, want %q", label, got, want)
		}
	}
}
