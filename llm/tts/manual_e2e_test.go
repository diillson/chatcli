//go:build manual_e2e

/*
 * ChatCLI - Embedded TTS manual end-to-end test.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Excluded from CI by build tag: it downloads the real sherpa-onnx engine and
 * Kokoro model (~150MB, one-time per cache dir) and synthesizes real audio.
 * Run it before touching the pinned versions or the voice catalog:
 *
 *   CHATCLI_TTS_CACHE_DIR=/tmp/tts-e2e \
 *     go test ./llm/tts/ -tags manual_e2e -run TestManualE2E -v -timeout 30m
 */
package tts

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

func TestManualE2E_EmbeddedProvisionAndSynthesize(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	e := NewEmbedded("", "", logger)

	outDir := os.Getenv("CHATCLI_TTS_CACHE_DIR")
	if outDir == "" {
		t.Fatal("set CHATCLI_TTS_CACHE_DIR explicitly so the download lands where you expect")
	}

	cases := []struct {
		name, text, format string
	}{
		{"english-ogg", "Good evening sir. All systems are now fully operational.", "ogg"},
		{"portuguese-ogg", "Boa noite senhor. Todos os sistemas estão totalmente operacionais.", "ogg"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			audio, err := e.Synthesize(context.Background(), tc.text, "", tc.format)
			if err != nil {
				t.Fatalf("Synthesize: %v", err)
			}
			if len(audio.Data) < 4096 {
				t.Fatalf("suspiciously small clip: %d bytes", len(audio.Data))
			}
			out := filepath.Join(outDir, tc.name+"."+audio.Ext)
			if err := os.WriteFile(out, audio.Data, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Logf("wrote %s (%d bytes, %s) — listen to confirm the voice", out, len(audio.Data), audio.Mime)
		})
	}
}
