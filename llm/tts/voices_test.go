/*
 * ChatCLI - Kokoro voice catalog tests.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package tts

import "testing"

func TestVoiceInfo_KnownVoices(t *testing.T) {
	tests := []struct {
		name string
		sid  int
		lang string
	}{
		{"bm_george", 26, "en"},
		{"bm_daniel", 24, "en"},
		{"bm_fable", 25, "en"},
		{"bm_lewis", 27, "en"},
		{"pm_alex", 43, "pt-br"},
		{"pm_santa", 44, "pt-br"},
		{"pf_dora", 42, "pt-br"},
		{"af_alloy", 0, "en"},
		{"zm_yunyang", 52, "zh"},
	}
	for _, tt := range tests {
		v, ok := voiceInfo(tt.name)
		if !ok {
			t.Errorf("voiceInfo(%q): not found", tt.name)
			continue
		}
		if v.sid != tt.sid || v.lang != tt.lang {
			t.Errorf("voiceInfo(%q) = sid %d lang %q, want sid %d lang %q",
				tt.name, v.sid, v.lang, tt.sid, tt.lang)
		}
	}
}

func TestVoiceInfo_UnknownVoice(t *testing.T) {
	if _, ok := voiceInfo("hal_9000"); ok {
		t.Fatal("unknown voice must not resolve")
	}
}

// The catalog must stay a dense 0..52 sid range with unique ids — a gap or a
// duplicate means a typo that would route to the wrong speaker.
func TestKokoroVoices_DenseUniqueSids(t *testing.T) {
	const total = 53
	if len(kokoroVoices) != total {
		t.Fatalf("catalog has %d voices, want %d", len(kokoroVoices), total)
	}
	seen := make(map[int]string, total)
	for name, v := range kokoroVoices {
		if v.sid < 0 || v.sid >= total {
			t.Errorf("voice %q sid %d out of range", name, v.sid)
		}
		if prev, dup := seen[v.sid]; dup {
			t.Errorf("voices %q and %q share sid %d", prev, name, v.sid)
		}
		seen[v.sid] = name
		if v.lang == "" {
			t.Errorf("voice %q has empty lang", name)
		}
	}
}

func TestDefaultEmbeddedVoicesExist(t *testing.T) {
	for _, name := range []string{defaultEmbeddedEnVoice, defaultEmbeddedPtVoice} {
		if _, ok := voiceInfo(name); !ok {
			t.Errorf("default voice %q missing from catalog", name)
		}
	}
}
