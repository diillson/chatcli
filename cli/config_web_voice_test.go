/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/i18n"
)

// /config web shows which speech engine the page uses per direction and
// the embedded engine's state, including an engine pinned by env.
func TestShowConfigWeb_VoiceRows(t *testing.T) {
	for _, k := range []string{"CHATCLI_TRANSCRIPTION_CMD", "CHATCLI_TRANSCRIPTION_URL", "CHATCLI_TTS_CMD", "CHATCLI_TTS_PROVIDER"} {
		t.Setenv(k, "")
	}
	t.Setenv("CHATCLI_TRANSCRIPTION_PROVIDER", "url")
	t.Setenv("CHATCLI_TRANSCRIPTION_URL", "http://127.0.0.1:9/v1")
	t.Setenv("CHATCLI_TRANSCRIPTION_CACHE_DIR", t.TempDir())
	t.Setenv("CHATCLI_TTS_CACHE_DIR", t.TempDir())
	c := &ChatCLI{}
	out := captureStdout(t, func() { c.showConfigWeb() })
	for _, want := range []string{i18n.T("cfg.web.voice_in"), i18n.T("cfg.web.voice_out"), i18n.T("cfg.web.voice_offline"),
		"selfhosted:", i18n.T("cfg.web.voice_src_env"), "embedded:whisper/", "embedded:kokoro/"} {
		if !strings.Contains(out, want) {
			t.Fatalf("panorama lacks %q: %q", want, out)
		}
	}
}

func TestWebVoiceLabels(t *testing.T) {
	if got := webVoiceActive("", "none"); got != i18n.T("cfg.web.voice_active", i18n.T("cfg.web.voice_none"), i18n.T("cfg.web.voice_src_none")) {
		t.Fatalf("active label = %q", got)
	}
	for _, c := range []struct {
		supported, installed bool
		want                 string
	}{{false, false, i18n.T("cfg.web.voice_unsupported")}, {true, true, i18n.T("cfg.web.voice_installed", "e")}, {true, false, i18n.T("cfg.web.voice_missing", "e", 233)}} {
		if got := webVoiceEmbedded("e", c.supported, c.installed, 233_472_211); got != c.want {
			t.Errorf("embedded label(%v,%v) = %q, want %s", c.supported, c.installed, got, c.want)
		}
	}
}
