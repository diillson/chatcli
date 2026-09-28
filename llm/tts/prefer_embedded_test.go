/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package tts

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/diillson/chatcli/llm/internal/provision"
)

// clearVoiceEnv is clearTTSEnv plus every cloud key the automatic chain
// reads and no system voice on PATH.
func clearVoiceEnv(t *testing.T) {
	t.Helper()
	clearTTSEnv(t)
	for _, k := range []string{"GROQ_API_KEY", "GOOGLEAI_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY"} {
		t.Setenv(k, "")
	}
	stubLookPath(t, nil)
}

func ttsPlatformSupported() bool {
	_, ok := provision.SherpaAsset(runtime.GOOS, runtime.GOARCH)
	return ok
}

func TestExplicitTTS(t *testing.T) {
	cases := []struct {
		env  map[string]string
		want bool
	}{
		{map[string]string{}, false},
		{map[string]string{"CHATCLI_TTS_PROVIDER": "AUTO"}, false},
		{map[string]string{"OPENAI_API_KEY": "sk-test"}, false},
		{map[string]string{"CHATCLI_TTS_PROVIDER": "embedded"}, true},
		{map[string]string{"CHATCLI_TTS_CMD": "say {text} -o {output}"}, true},
		{map[string]string{"CHATCLI_TTS_URL": "http://127.0.0.1:9/v1"}, true},
	}
	for _, c := range cases {
		clearVoiceEnv(t)
		for k, v := range c.env {
			t.Setenv(k, v)
		}
		if got := Explicit(); got != c.want {
			t.Errorf("Explicit(%v) = %v, want %v", c.env, got, c.want)
		}
	}
}

// Nothing configured, nothing installed: a system voice is only the
// fallback while the embedded install is on offer.
func TestSelectPreferEmbeddedTTS_FallbackAndOffer(t *testing.T) {
	if !ttsPlatformSupported() {
		t.Skip("no prebuilt engine for this platform")
	}
	clearVoiceEnv(t)
	stubLookPath(t, map[string]string{"espeak-ng": "/usr/bin/espeak-ng"})
	sel := SelectPreferEmbedded(nil)
	if !sel.PreferEmbedded || sel.Source != SourceAuto || sel.Active.Name() != "local:espeak-ng" {
		t.Fatalf("selection = %+v", sel)
	}
	want := provision.SherpaAssetBytes(runtime.GOOS, runtime.GOARCH) + kokoroArchiveBytes
	if sel.Embedded.Installed || sel.Embedded.DownloadBytes != want || sel.NewEmbedded == nil {
		t.Fatalf("embedded = %+v, want %d bytes", sel.Embedded, want)
	}
	if _, ok := sel.NewEmbedded().(Provisioner); !ok {
		t.Fatal("the embedded voice must be installable up front")
	}

	stubLookPath(t, nil)
	sel = SelectPreferEmbedded(nil)
	if !IsNull(sel.Active) || sel.Source != SourceNone {
		t.Fatalf("no fallback selection = %+v", sel)
	}
}

// Explicit choices win; a pinned embedded voice that is not installed waits
// for the install instead of downloading on the first click.
func TestSelectPreferEmbeddedTTS_Explicit(t *testing.T) {
	clearVoiceEnv(t)
	t.Setenv("CHATCLI_TTS_PROVIDER", "embedded")
	sel := SelectPreferEmbedded(nil)
	if !IsNull(sel.Active) || sel.Source != SourceEnv || sel.PreferEmbedded {
		t.Fatalf("pinned selection = %+v", sel)
	}
	t.Setenv("CHATCLI_TTS_PROVIDER", "")
	t.Setenv("CHATCLI_TTS_URL", "http://127.0.0.1:9/v1")
	if sel = SelectPreferEmbedded(nil); sel.Source != SourceEnv || IsNull(sel.Active) {
		t.Fatalf("url selection = %+v", sel)
	}
}

// Installed, the embedded voice is active; a partial cache counts only the
// missing model.
func TestSelectPreferEmbeddedTTS_InstalledAndPartial(t *testing.T) {
	if !ttsPlatformSupported() {
		t.Skip("no prebuilt engine for this platform")
	}
	clearVoiceEnv(t)
	root, _ := provisionFakeCache(t)
	t.Setenv("CHATCLI_TTS_CACHE_DIR", root)
	sel := SelectPreferEmbedded(nil)
	if sel.Source != SourceEmbedded || !sel.Embedded.Installed {
		t.Fatalf("selection = %+v", sel)
	}
	if info, ok := DescribeEmbedded(sel.Active); !ok || !info.Installed || info.DownloadBytes != 0 {
		t.Fatalf("active info = %+v", info)
	}
	if err := sel.Active.(Provisioner).EnsureReady(context.Background()); err != nil {
		t.Fatalf("an installed engine is ready: %v", err)
	}
	if _, ok := DescribeEmbedded(NewNull()); ok {
		t.Fatal("Null is not the embedded engine")
	}

	partial := t.TempDir()
	if err := os.MkdirAll(filepath.Join(partial, "sherpa-v"+sherpaVersion), 0o750); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CHATCLI_TTS_CACHE_DIR", partial)
	if info, _ := DescribeEmbedded(NewEmbeddedFromEnv(nil)); info.DownloadBytes != kokoroArchiveBytes {
		t.Fatalf("partial info = %+v", info)
	}
	ctx := context.Background()
	if WithProvisionProgress(ctx, nil) != ctx || WithProvisionProgress(ctx, func(ProvisionProgress) {}) == ctx {
		t.Fatal("progress hook wiring")
	}
}
