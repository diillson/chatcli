/*
 * ChatCLI - tests for the transcription provider factory.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package transcription

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// clearEnv neutralizes every env var the factory reads AND stubs the PATH
// lookup to "no local whisper", so a subtest starts from a known state
// regardless of the host environment (e.g. a dev box with whisper-cli installed).
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"CHATCLI_TRANSCRIPTION_URL",
		"CHATCLI_TRANSCRIPTION_KEY",
		"CHATCLI_TRANSCRIPTION_CMD",
		"CHATCLI_TRANSCRIPTION_PROVIDER",
		"CHATCLI_TRANSCRIPTION_MODEL",
		"CHATCLI_TRANSCRIPTION_LANG",
		"OPENAI_API_KEY",
		"GROQ_API_KEY",
		"WHISPER_MODEL",
	} {
		t.Setenv(k, "")
	}
	// Point the embedded cache at an empty temp dir so a dev box with a real
	// provisioned ~/.cache/chatcli/stt never changes auto-detection results.
	t.Setenv("CHATCLI_TRANSCRIPTION_CACHE_DIR", t.TempDir())
	orig := execLookPath
	execLookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	t.Cleanup(func() { execLookPath = orig })
}

func TestNewFromEnv_Selection(t *testing.T) {
	log := zap.NewNop()

	t.Run("self-hosted URL is keyless and wins", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("CHATCLI_TRANSCRIPTION_URL", "http://localhost:8080/v1")
		p := NewFromEnv(log)
		if IsNull(p) {
			t.Fatal("URL should yield a real provider")
		}
		if !strings.HasPrefix(p.Name(), "selfhosted:") {
			t.Errorf("Name = %q, want selfhosted:*", p.Name())
		}
	})

	t.Run("local command is keyless and wins in auto", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("CHATCLI_TRANSCRIPTION_CMD", "whisper-cli -nt -f {input}")
		t.Setenv("CHATCLI_TRANSCRIPTION_URL", "http://localhost:8080/v1")
		t.Setenv("OPENAI_API_KEY", "sk-test")
		p := NewFromEnv(log)
		if IsNull(p) || !strings.HasPrefix(p.Name(), "command:") {
			t.Errorf("command must win local-first; got null=%v name=%q", IsNull(p), name(p))
		}
	})

	t.Run("explicit command needs the template", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("CHATCLI_TRANSCRIPTION_PROVIDER", "command")
		if !IsNull(NewFromEnv(log)) {
			t.Error("command without CHATCLI_TRANSCRIPTION_CMD must be null")
		}
	})

	t.Run("local whisper on PATH is keyless and beats cloud", func(t *testing.T) {
		clearEnv(t)
		execLookPath = func(name string) (string, error) {
			if name == "whisper-cli" {
				return "/opt/homebrew/bin/whisper-cli", nil
			}
			return "", exec.ErrNotFound
		}
		t.Setenv("OPENAI_API_KEY", "sk-test") // must NOT be used: a local engine exists
		p := NewFromEnv(log)
		if IsNull(p) || !strings.HasPrefix(p.Name(), "local:") {
			t.Errorf("local whisper must win over cloud; got %q", name(p))
		}
	})

	t.Run("auto prefers groq (free) over openai (paid)", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("GROQ_API_KEY", "gsk-test")
		t.Setenv("OPENAI_API_KEY", "sk-test")
		if p := NewFromEnv(log); IsNull(p) || !strings.HasPrefix(p.Name(), "groq:") {
			t.Errorf("auto must prefer groq; got %q", name(p))
		}
	})

	t.Run("explicit openai needs the key", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("CHATCLI_TRANSCRIPTION_PROVIDER", "openai")
		if !IsNull(NewFromEnv(log)) {
			t.Error("openai without OPENAI_API_KEY must be null")
		}
		t.Setenv("OPENAI_API_KEY", "sk-test")
		if p := NewFromEnv(log); IsNull(p) || !strings.HasPrefix(p.Name(), "openai:") {
			t.Errorf("openai with key should select openai; got null=%v name=%q", IsNull(p), name(p))
		}
	})

	t.Run("explicit groq needs the key and defaults its model", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("CHATCLI_TRANSCRIPTION_PROVIDER", "groq")
		if !IsNull(NewFromEnv(log)) {
			t.Error("groq without GROQ_API_KEY must be null")
		}
		t.Setenv("GROQ_API_KEY", "gsk-test")
		p := NewFromEnv(log)
		if IsNull(p) || p.Name() != "groq:"+groqDefaultModel {
			t.Errorf("groq Name = %q, want groq:%s", name(p), groqDefaultModel)
		}
	})

	t.Run("zero-config reuses OPENAI_API_KEY", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("OPENAI_API_KEY", "sk-test")
		if p := NewFromEnv(log); IsNull(p) || !strings.HasPrefix(p.Name(), "openai:") {
			t.Errorf("auto-detect should reuse OPENAI key; got null=%v name=%q", IsNull(p), name(p))
		}
	})

	t.Run("nothing configured defaults to embedded (zero-config voice)", func(t *testing.T) {
		clearEnv(t)
		p := NewFromEnv(log)
		if IsNull(p) || !strings.HasPrefix(p.Name(), "embedded:whisper/") {
			t.Errorf("empty environment must fall back to embedded whisper; got %q", name(p))
		}
	})

	t.Run("explicit embedded provisions lazily, no env needed", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("CHATCLI_TRANSCRIPTION_PROVIDER", "embedded")
		p := NewFromEnv(log)
		if IsNull(p) || p.Name() != "embedded:whisper/"+defaultEmbeddedWhisperSize {
			t.Errorf("embedded pin must yield the embedded provider; got %q", name(p))
		}
	})

	t.Run("auto uses embedded only when already provisioned", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("fixture uses shell script")
		}
		clearEnv(t)
		t.Setenv("OPENAI_API_KEY", "sk-test") // must NOT be used: a local engine exists
		root, _ := provisionFakeSTTCache(t)
		t.Setenv("CHATCLI_TRANSCRIPTION_CACHE_DIR", root)
		p := NewFromEnv(log)
		if IsNull(p) || !strings.HasPrefix(p.Name(), "embedded:whisper/") {
			t.Errorf("provisioned embedded cache must win in auto; got %q", name(p))
		}
	})

	t.Run("unknown provider is null", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("CHATCLI_TRANSCRIPTION_PROVIDER", "bogus")
		t.Setenv("OPENAI_API_KEY", "sk-test") // must NOT be used: explicit choice was unknown
		if !IsNull(NewFromEnv(log)) {
			t.Error("unknown provider must be null, not a silent fallback")
		}
	})
}

func name(p Provider) string {
	if p == nil {
		return "<nil>"
	}
	return p.Name()
}
