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

// stubNoFFmpeg forces the "ffmpeg not installed" branch.
func stubNoFFmpeg(t *testing.T) {
	t.Helper()
	orig := hasFFmpegTTS
	hasFFmpegTTS = func() bool { return false }
	t.Cleanup(func() { hasFFmpegTTS = orig })
}

// installFakeFFmpeg places an executable named ffmpeg at the front of PATH —
// the same resolution the production code uses. It consumes stdin and writes
// `output` to stdout, mirroring the streaming transcode contract. An empty
// output makes it exit non-zero instead.
func installFakeFFmpeg(t *testing.T, output string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake ffmpeg is a shell script")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\ncat > /dev/null\nprintf '" + output + "'\n"
	if output == "" {
		script = "#!/bin/sh\ncat > /dev/null\nexit 1\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "ffmpeg"), []byte(script), 0o755); err != nil { // #nosec G306 -- test fixture must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestToVoiceNote_TranscodesRawFormats(t *testing.T) {
	installFakeFFmpeg(t, "OggSnote")
	for _, ext := range []string{"wav", "aiff"} {
		in := Audio{Data: []byte("RIFFraw"), Mime: "audio/" + ext, Ext: ext}
		got := ToVoiceNote(context.Background(), in, nil)
		if got.Ext != "ogg" || got.Mime != "audio/ogg" || string(got.Data) != "OggSnote" {
			t.Errorf("ToVoiceNote(%s) = ext %q mime %q data %q, want transcoded ogg", ext, got.Ext, got.Mime, got.Data)
		}
	}
}

func TestToVoiceNote_PassesThroughCompressedFormats(t *testing.T) {
	installFakeFFmpeg(t, "") // would fail if ever invoked
	for _, ext := range []string{"ogg", "mp3", "aac", "flac"} {
		in := Audio{Data: []byte("compressed"), Mime: "audio/x", Ext: ext}
		if got := ToVoiceNote(context.Background(), in, nil); string(got.Data) != "compressed" || got.Ext != ext {
			t.Errorf("ToVoiceNote(%s) must pass through unchanged, got ext %q", ext, got.Ext)
		}
	}
}

func TestToVoiceNote_NoFFmpegReturnsOriginal(t *testing.T) {
	stubNoFFmpeg(t)
	in := Audio{Data: []byte("RIFFraw"), Mime: "audio/aiff", Ext: "aiff"}
	if got := ToVoiceNote(context.Background(), in, nil); got.Ext != "aiff" || string(got.Data) != "RIFFraw" {
		t.Fatalf("without ffmpeg the clip must be unchanged, got ext %q", got.Ext)
	}
}

func TestToVoiceNote_FailedTranscodeReturnsOriginal(t *testing.T) {
	installFakeFFmpeg(t, "") // fake exits 1
	in := Audio{Data: []byte("RIFFraw"), Mime: "audio/wav", Ext: "wav"}
	if got := ToVoiceNote(context.Background(), in, nil); got.Ext != "wav" {
		t.Fatalf("failed transcode must return original, got ext %q", got.Ext)
	}
}
