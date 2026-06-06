/*
 * ChatCLI - Language routing tests.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package tts

import "testing"

func TestDetectLang(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"pt with diacritics", "Não se preocupe, está tudo sob controle.", "pt"},
		{"pt without diacritics", "tudo certo, pode fazer isso que o sistema vai funcionar bem", "pt"},
		{"en plain", "All systems are operational and ready for your command.", "en"},
		{"en technical", "The build is done and all tests have passed.", "en"},
		{"empty defaults to en", "", "en"},
		{"numbers default to en", "42 100 7", "en"},
		{"pt question", "você já verificou como ele está?", "pt"},
		{"en question", "have you checked what is going on here?", "en"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectLang(tt.in); got != tt.want {
				t.Errorf("detectLang(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestPickVoice(t *testing.T) {
	if got := pickVoice("Boa noite senhor, tudo está pronto.", "bm_george", "pm_alex"); got != "pm_alex" {
		t.Errorf("pt text routed to %q, want pm_alex", got)
	}
	if got := pickVoice("Good evening sir, everything is ready.", "bm_george", "pm_alex"); got != "bm_george" {
		t.Errorf("en text routed to %q, want bm_george", got)
	}
	if got := pickVoice("", "bm_george", "pm_alex"); got != "bm_george" {
		t.Errorf("empty text routed to %q, want bm_george", got)
	}
}
