/*
 * ChatCLI - Voice-note transcoding tests.
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
)

func stubFFmpeg(t *testing.T, path string) {
	t.Helper()
	orig := lookupFFmpegTTS
	lookupFFmpegTTS = func() string { return path }
	t.Cleanup(func() { lookupFFmpegTTS = orig })
}

func fakeFFmpegScript(t *testing.T, output string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake ffmpeg is a shell script")
	}
	fake := filepath.Join(t.TempDir(), "fakeffmpeg")
	script := "#!/bin/sh\nfor a in \"$@\"; do out=\"$a\"; done\nprintf '" + output + "' > \"$out\"\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil { // #nosec G306 -- test fixture must be executable
		t.Fatal(err)
	}
	return fake
}

func TestToVoiceNote_TranscodesRawFormats(t *testing.T) {
	stubFFmpeg(t, fakeFFmpegScript(t, "OggSnote"))
	for _, ext := range []string{"wav", "aiff"} {
		in := Audio{Data: []byte("RIFFraw"), Mime: "audio/" + ext, Ext: ext}
		got := ToVoiceNote(context.Background(), in, nil)
		if got.Ext != "ogg" || got.Mime != "audio/ogg" || string(got.Data) != "OggSnote" {
			t.Errorf("ToVoiceNote(%s) = ext %q mime %q data %q, want transcoded ogg", ext, got.Ext, got.Mime, got.Data)
		}
	}
}

func TestToVoiceNote_PassesThroughCompressedFormats(t *testing.T) {
	stubFFmpeg(t, "/must/not/run")
	for _, ext := range []string{"ogg", "mp3", "aac", "flac"} {
		in := Audio{Data: []byte("compressed"), Mime: "audio/x", Ext: ext}
		if got := ToVoiceNote(context.Background(), in, nil); string(got.Data) != "compressed" || got.Ext != ext {
			t.Errorf("ToVoiceNote(%s) must pass through unchanged, got ext %q", ext, got.Ext)
		}
	}
}

func TestToVoiceNote_NoFFmpegReturnsOriginal(t *testing.T) {
	stubFFmpeg(t, "")
	in := Audio{Data: []byte("RIFFraw"), Mime: "audio/aiff", Ext: "aiff"}
	if got := ToVoiceNote(context.Background(), in, nil); got.Ext != "aiff" || string(got.Data) != "RIFFraw" {
		t.Fatalf("without ffmpeg the clip must be unchanged, got ext %q", got.Ext)
	}
}

func TestToVoiceNote_FailedTranscodeReturnsOriginal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake ffmpeg is a shell script")
	}
	fake := filepath.Join(t.TempDir(), "failffmpeg")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil { // #nosec G306 -- test fixture must be executable
		t.Fatal(err)
	}
	stubFFmpeg(t, fake)
	in := Audio{Data: []byte("RIFFraw"), Mime: "audio/wav", Ext: "wav"}
	if got := ToVoiceNote(context.Background(), in, nil); got.Ext != "wav" {
		t.Fatalf("failed transcode must return original, got ext %q", got.Ext)
	}
}
