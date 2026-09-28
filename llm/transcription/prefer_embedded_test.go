/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package transcription

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/diillson/chatcli/llm/internal/provision"
)

func TestExplicit(t *testing.T) {
	cases := []struct {
		env  map[string]string
		want bool
	}{
		{map[string]string{}, false},
		{map[string]string{"CHATCLI_TRANSCRIPTION_PROVIDER": "auto"}, false},
		{map[string]string{"OPENAI_API_KEY": "sk-test", "GROQ_API_KEY": "gsk-test"}, false},
		{map[string]string{"CHATCLI_TRANSCRIPTION_PROVIDER": "openai"}, true},
		{map[string]string{"CHATCLI_TRANSCRIPTION_PROVIDER": "Embedded"}, true},
		{map[string]string{"CHATCLI_TRANSCRIPTION_CMD": "whisper {input}"}, true},
		{map[string]string{"CHATCLI_TRANSCRIPTION_URL": "http://127.0.0.1:9/v1"}, true},
	}
	for _, c := range cases {
		clearEnv(t)
		for k, v := range c.env {
			t.Setenv(k, v)
		}
		if got := Explicit(); got != c.want {
			t.Errorf("Explicit(%v) = %v, want %v", c.env, got, c.want)
		}
	}
}

func platformSupported() bool {
	_, ok := provision.SherpaAsset(runtime.GOOS, runtime.GOARCH)
	return ok
}

// With nothing configured and nothing installed, a cloud key is only the
// fallback: the selection prefers the embedded install and counts the whole
// download.
func TestSelectPreferEmbedded_NotInstalledFallsBackToAutoChain(t *testing.T) {
	if !platformSupported() {
		t.Skip("no prebuilt engine for this platform")
	}
	clearEnv(t)
	t.Setenv("OPENAI_API_KEY", "sk-test")
	sel := SelectPreferEmbedded(nil)
	if !sel.PreferEmbedded || sel.Source != SourceAuto || sel.Active == nil || !strings.HasPrefix(sel.Active.Name(), "openai:") {
		t.Fatalf("selection = %+v (active %v)", sel, sel.Active)
	}
	want := provision.SherpaAssetBytes(runtime.GOOS, runtime.GOARCH) + whisperArchiveBytes["base"]
	if sel.Embedded.Installed || !sel.Embedded.Supported || sel.Embedded.DownloadBytes != want {
		t.Fatalf("embedded = %+v, want %d bytes", sel.Embedded, want)
	}
	if sel.NewEmbedded == nil || sel.NewEmbedded().Name() != "embedded:whisper/base" {
		t.Fatal("the install needs a fresh embedded provider")
	}
}

// With nothing configured at all, the automatic chain's last resort is the
// embedded engine downloading on first use: the selection refuses it so the
// surface asks first.
func TestSelectPreferEmbedded_NeverHandsOutAPendingDownload(t *testing.T) {
	if !platformSupported() {
		t.Skip("no prebuilt engine for this platform")
	}
	clearEnv(t)
	sel := SelectPreferEmbedded(nil)
	if !IsNull(sel.Active) || sel.Source != SourceNone || !sel.PreferEmbedded {
		t.Fatalf("selection = %+v", sel)
	}
	// Pinned by env, the embedded engine still waits for the install.
	t.Setenv("CHATCLI_TRANSCRIPTION_PROVIDER", "embedded")
	sel = SelectPreferEmbedded(nil)
	if !IsNull(sel.Active) || sel.Source != SourceEnv || sel.PreferEmbedded {
		t.Fatalf("pinned selection = %+v", sel)
	}
}

// An explicit backend always wins over the embedded preference.
func TestSelectPreferEmbedded_ExplicitWins(t *testing.T) {
	clearEnv(t)
	t.Setenv("CHATCLI_TRANSCRIPTION_URL", "http://127.0.0.1:9/v1")
	sel := SelectPreferEmbedded(nil)
	if sel.Source != SourceEnv || sel.PreferEmbedded || !strings.HasPrefix(sel.Active.Name(), "selfhosted:") {
		t.Fatalf("selection = %+v", sel)
	}
}

// Installed in the cache, the embedded engine is the active one, and a
// partial cache only counts the pieces still missing.
func TestSelectPreferEmbedded_InstalledAndPartial(t *testing.T) {
	if !platformSupported() {
		t.Skip("no prebuilt engine for this platform")
	}
	clearEnv(t)
	root, _ := provisionFakeSTTCache(t)
	t.Setenv("CHATCLI_TRANSCRIPTION_CACHE_DIR", root)
	t.Setenv("OPENAI_API_KEY", "sk-test")
	sel := SelectPreferEmbedded(nil)
	if sel.Source != SourceEmbedded || !sel.Embedded.Installed || sel.Embedded.DownloadBytes != 0 {
		t.Fatalf("selection = %+v", sel)
	}
	if info, ok := DescribeEmbedded(sel.Active); !ok || !info.Installed {
		t.Fatalf("active is not the installed embedded engine: %+v", info)
	}
	if _, ok := DescribeEmbedded(NewNull()); ok {
		t.Fatal("Null is not the embedded engine")
	}

	partial := t.TempDir()
	if err := os.MkdirAll(filepath.Join(partial, "sherpa-v"+provision.SherpaVersion), 0o750); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CHATCLI_TRANSCRIPTION_CACHE_DIR", partial)
	t.Setenv("CHATCLI_TRANSCRIPTION_MODEL", "tiny")
	info, _ := DescribeEmbedded(NewEmbeddedFromEnv(nil))
	if info.Installed || info.DownloadBytes != whisperArchiveBytes["tiny"] || info.Name != "embedded:whisper/tiny" {
		t.Fatalf("partial cache info = %+v", info)
	}
}

func TestWithProvisionProgress(t *testing.T) {
	ctx := context.Background()
	if WithProvisionProgress(ctx, nil) != ctx {
		t.Fatal("a nil hook must leave the context untouched")
	}
	if WithProvisionProgress(ctx, func(ProvisionProgress) {}) == ctx {
		t.Fatal("a hook must be attached")
	}
}
