/*
 * ChatCLI - TTS factory tests.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package tts

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
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
	for _, k := range []string{"CHATCLI_TTS_PROVIDER", "CHATCLI_TTS_CMD", "CHATCLI_TTS_URL", "CHATCLI_TTS_KEY", "CHATCLI_TTS_MODEL", "CHATCLI_TTS_VOICE", "CHATCLI_TTS_VOICE_PT", "OPENAI_API_KEY"} {
		t.Setenv(k, "")
	}
	// Point the embedded cache at an empty dir so auto-detection never sees a
	// developer machine's real provisioned engine.
	t.Setenv("CHATCLI_TTS_CACHE_DIR", t.TempDir())
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

func TestFactory_EmbeddedPin(t *testing.T) {
	clearTTSEnv(t)
	t.Setenv("CHATCLI_TTS_PROVIDER", "embedded")
	p := NewFromEnv(zap.NewNop())
	if IsNull(p) {
		t.Fatal("embedded pin must build the provider for lazy provisioning, got Null")
	}
	if p.Name() != "embedded:kokoro/bm_george" {
		t.Fatalf("Name = %q, want embedded:kokoro/bm_george", p.Name())
	}

	t.Setenv("CHATCLI_TTS_PROVIDER", "kokoro") // alias
	if p := NewFromEnv(zap.NewNop()); IsNull(p) {
		t.Fatal("kokoro alias must build the embedded provider")
	}
}

func TestFactory_EmbeddedPinHonorsVoices(t *testing.T) {
	clearTTSEnv(t)
	t.Setenv("CHATCLI_TTS_PROVIDER", "embedded")
	t.Setenv("CHATCLI_TTS_VOICE", "bm_lewis")
	p := NewFromEnv(zap.NewNop())
	if p.Name() != "embedded:kokoro/bm_lewis" {
		t.Fatalf("Name = %q, want embedded:kokoro/bm_lewis", p.Name())
	}
}

func TestFactory_AutoSkipsUnprovisionedEmbedded(t *testing.T) {
	clearTTSEnv(t)
	stubLookPath(t, nil)
	// Empty cache: auto-detection must not pick embedded nor download anything.
	if p := NewFromEnv(zap.NewNop()); !IsNull(p) {
		t.Fatalf("expected Null with empty cache and no CLIs, got %s", p.Name())
	}
}

func TestFactory_AutoPrefersProvisionedEmbeddedOverSay(t *testing.T) {
	clearTTSEnv(t)
	stubLookPath(t, map[string]string{"say": "/usr/bin/say"})

	// An empty cache falls back to `say`.
	p := NewFromEnv(zap.NewNop())
	if _, ok := p.(*CommandSynthesizer); !ok {
		t.Fatalf("expected *CommandSynthesizer fallback, got %T", p)
	}

	// A provisioned cache wins over `say`.
	seedProvisionedCache(t, os.Getenv("CHATCLI_TTS_CACHE_DIR"))
	p = NewFromEnv(zap.NewNop())
	if _, ok := p.(*embeddedSynth); !ok {
		t.Fatalf("expected *embeddedSynth from provisioned cache, got %T", p)
	}
}

// seedProvisionedCache lays out the minimal artifacts isProvisionedDir checks.
func seedProvisionedCache(t *testing.T, root string) {
	t.Helper()
	binName := sherpaBinName
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	mustWrite(t, filepath.Join(root, "sherpa-v"+sherpaVersion, "bin", binName), "bin")
	mustWrite(t, filepath.Join(root, "kokoro", "voices.bin"), "v")
	mustWrite(t, filepath.Join(root, "kokoro", "tokens.txt"), "t")
	mustWrite(t, filepath.Join(root, "kokoro", "model.int8.onnx"), "m")
	if err := os.MkdirAll(filepath.Join(root, "kokoro", "espeak-ng-data"), 0o750); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, readyMarker(root), "")
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

func TestFactory_GroqAndURLAndLocalEspeak(t *testing.T) {
	clearTTSEnv(t)
	t.Setenv("GROQ_API_KEY", "")
	// groq pin without key → null
	t.Setenv("CHATCLI_TTS_PROVIDER", "groq")
	if !IsNull(NewFromEnv(zap.NewNop())) {
		t.Fatal("groq pin without key should be Null")
	}
	// groq pin with key → openai-compatible (label groq)
	t.Setenv("GROQ_API_KEY", "g")
	if NewFromEnv(zap.NewNop()).Name() != "groq" {
		t.Fatal("expected groq backend")
	}
	// url pin without url → null
	clearTTSEnv(t)
	t.Setenv("CHATCLI_TTS_PROVIDER", "url")
	if !IsNull(NewFromEnv(zap.NewNop())) {
		t.Fatal("url pin without URL should be Null")
	}
	// unknown provider → null
	t.Setenv("CHATCLI_TTS_PROVIDER", "bogus")
	if !IsNull(NewFromEnv(zap.NewNop())) {
		t.Fatal("unknown provider should be Null")
	}
}

func TestFactory_AutoLocalEspeakAndGroqFallback(t *testing.T) {
	clearTTSEnv(t)
	stubLookPath(t, map[string]string{"espeak-ng": "/usr/bin/espeak-ng"})
	if _, ok := NewFromEnv(zap.NewNop()).(*CommandSynthesizer); !ok {
		t.Fatal("expected local espeak-ng command backend")
	}
	// no local, groq key present → groq cloud fallback
	clearTTSEnv(t)
	stubLookPath(t, nil)
	t.Setenv("GROQ_API_KEY", "g")
	if NewFromEnv(zap.NewNop()).Name() != "groq" {
		t.Fatal("expected groq fallback")
	}
}
