/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package webui

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/i18n"
)

// The page converts every recording to 16 kHz mono PCM WAV before it leaves
// the browser (no system decoder anywhere), drives the embedded install
// through the voice endpoints, and wires the conversation controls.
func TestPage_VoicePipeline(t *testing.T) {
	page := string(Page())
	voice := page[strings.Index(page, "// ---------- voice: embedded engines"):strings.Index(page, "if (BOOT.features.images) {")]
	for _, want := range []string{
		// recording → WAV in the browser
		"decodeAudioData", "OfflineAudioContext", "const WAV_RATE = 16000", "'audio/wav'", "type: 'audio/wav'",
		"setUint16(20, 1, true)", "setUint16(22, 1, true)", "setUint16(34, 16, true)", "'/api/stt?filename='",
		// visible states and error reasons
		"micUI('rec')", "micUI('busy')", "t('stt.transcribing')", "t('stt.denied')", "t('stt.empty')", "NotAllowedError",
		"if (!r.ok)", "j.error", "t('tts.failed')", "await a.play()",
		// embedded engine first use: status, install with progress, cancel
		"api('voice/status')", "api('voice/install'", "api('voice/cancel'", "t('voice.install.progress'", "t('voice.install.fallback'",
		// voice conversation: auto-send, spoken reply, barge-in, Esc
		"sendTurn(text)", "stopPlayback(); // barge-in", "watchSilence(stream)", "e.key !== 'Escape'", "stopRecording(true)",
	} {
		if !strings.Contains(voice, want) {
			t.Errorf("voice section lacks %q", want)
		}
	}
	// The old path posted the recorder's own container labeled as webm.
	if strings.Contains(page, "filename=voice.webm") {
		t.Error("the page must not label every recording as webm")
	}
	// A finished reply is read back in a voice conversation, never after a
	// cancelled or failed turn.
	for _, want := range []string{"if (voice.talk && !voice.quiet) speak(s.text", "voice.quiet = !!ev.cancelled", "case 'error': voice.quiet = true"} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	for _, id := range []string{`id="btnTalk"`, `id="voiceHint"`, `id="voiceModal"`, `id="voiceProg"`, `id="voiceDownload"`, `id="voiceCancelDl"`, `id="voiceFallback"`, `id="voiceStatus"`} {
		if !strings.Contains(page, id) {
			t.Errorf("markup lacks %s", id)
		}
	}
}

// Every voice string the page and the server show resolves in each locale.
func TestVoiceStrings_Translated(t *testing.T) {
	i18n.Init()
	keys := []string{"web.voice_install_required", "web.voice_install_cancelled", "web.voice_nothing_to_install", "web.voice_not_installable",
		"web.stt_format", "web.stt_failed", "web.tts_failed", "web.tts_empty", "web.ui.voice.install.body", "web.ui.stt.hint"}
	for _, lang := range UILanguages {
		for _, k := range keys {
			if v, ok := i18n.LookupIn(lang, k); !ok || v == "" || v == k {
				t.Errorf("%s: %s unresolved", lang, k)
			}
		}
	}
	en, _ := i18n.LookupIn("en", "web.ui.voice.talk")
	pt, _ := i18n.LookupIn("pt-BR", "web.ui.voice.talk")
	if en == pt {
		t.Errorf("voice.talk is not translated: %q", en)
	}
}
