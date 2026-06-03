/*
 * ChatCLI - TTS factory tests.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package tts

import (
	"errors"
	"testing"

	"go.uber.org/zap"
)

func stubLookPath(t *testing.T, found map[string]string) {
	t.Helper()
	old := execLookPath
	execLookPath = func(file string) (string, error) {
		if p, ok := found[file]; ok {
			return p, nil
		}
		return "", errors.New("not found")
	}
	t.Cleanup(func() { execLookPath = old })
}

func clearTTSEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"CHATCLI_TTS_PROVIDER", "CHATCLI_TTS_CMD", "CHATCLI_TTS_URL", "CHATCLI_TTS_KEY", "CHATCLI_TTS_MODEL", "OPENAI_API_KEY"} {
		t.Setenv(k, "")
	}
}

func TestFactory_NullWhenNothingConfigured(t *testing.T) {
	clearTTSEnv(t)
	stubLookPath(t, nil)
	if p := NewFromEnv(zap.NewNop()); !IsNull(p) {
		t.Fatalf("expected Null, got %s", p.Name())
	}
}

func TestFactory_CommandPin(t *testing.T) {
	clearTTSEnv(t)
	t.Setenv("CHATCLI_TTS_PROVIDER", "command")
	t.Setenv("CHATCLI_TTS_CMD", "say {text} -o {output}")
	p := NewFromEnv(zap.NewNop())
	if IsNull(p) {
		t.Fatal("expected command provider, got Null")
	}
	if _, ok := p.(*CommandSynthesizer); !ok {
		t.Fatalf("expected *CommandSynthesizer, got %T", p)
	}
}

func TestFactory_CommandPinMissingDegradesToNull(t *testing.T) {
	clearTTSEnv(t)
	t.Setenv("CHATCLI_TTS_PROVIDER", "command")
	if p := NewFromEnv(zap.NewNop()); !IsNull(p) {
		t.Fatalf("pinned command with no CMD should be Null, got %s", p.Name())
	}
}

func TestFactory_URLPin(t *testing.T) {
	clearTTSEnv(t)
	t.Setenv("CHATCLI_TTS_PROVIDER", "url")
	t.Setenv("CHATCLI_TTS_URL", "http://localhost:9999/v1")
	p := NewFromEnv(zap.NewNop())
	if _, ok := p.(*OpenAICompatible); !ok {
		t.Fatalf("expected *OpenAICompatible, got %T", p)
	}
}

func TestFactory_OpenAIPinWithoutKeyIsNull(t *testing.T) {
	clearTTSEnv(t)
	t.Setenv("CHATCLI_TTS_PROVIDER", "openai")
	if p := NewFromEnv(zap.NewNop()); !IsNull(p) {
		t.Fatalf("openai pin without key should be Null, got %s", p.Name())
	}
}

func TestFactory_AutoDetectsLocalSay(t *testing.T) {
	clearTTSEnv(t)
	stubLookPath(t, map[string]string{"say": "/usr/bin/say"})
	p := NewFromEnv(zap.NewNop())
	if IsNull(p) {
		t.Fatal("expected local say provider, got Null")
	}
	if _, ok := p.(*CommandSynthesizer); !ok {
		t.Fatalf("expected *CommandSynthesizer, got %T", p)
	}
}

func TestFactory_AutoOpenAIFallback(t *testing.T) {
	clearTTSEnv(t)
	stubLookPath(t, nil)
	t.Setenv("OPENAI_API_KEY", "sk-test")
	p := NewFromEnv(zap.NewNop())
	if p.Name() != "openai" {
		t.Fatalf("expected openai fallback, got %s", p.Name())
	}
}
