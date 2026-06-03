/*
 * ChatCLI - Local whisper.cpp transcription with model auto-provisioning.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * whisper.cpp's `whisper-cli` decodes ogg/mp3/flac/wav natively (no ffmpeg)
 * and prints the transcript to stdout, but it needs a ggml model file — which
 * `brew install whisper-cpp` does not provide. To make voice "just work" after
 * a plain install, this provider lazily downloads a ggml model to the cache on
 * first use (like faster-whisper auto-provisioning its model in hermes-agent),
 * then delegates execution to the generic command backend.
 */
package transcription

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

const (
	whisperCppModelBaseURL = "https://huggingface.co/ggerganov/whisper.cpp/resolve/main"
	defaultWhisperCppSize  = "base"
	minModelBytes          = 1 << 20          // a real ggml model is tens of MB; reject error pages
	modelDownloadTimeout   = 30 * time.Minute // a large model on a slow link
)

// localWhisperCpp runs whisper-cli, auto-provisioning its ggml model.
type localWhisperCpp struct {
	bin      string // path to whisper-cli
	size     string // model size keyword ("base", "small", …) or an explicit .bin path
	baseURL  string // model download base; overridable for tests
	cacheDir string // model cache dir; overridable for tests
	logger   *zap.Logger

	once     sync.Once
	model    string
	modelErr error
}

// newLocalWhisperCpp builds the provider. model is the raw CHATCLI_TRANSCRIPTION_MODEL
// value (a size keyword, a .bin path, or empty → "base"); WHISPER_MODEL overrides it.
func newLocalWhisperCpp(bin, model string, logger *zap.Logger) *localWhisperCpp {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &localWhisperCpp{
		bin:     bin,
		size:    chooseWhisperCppModel(model),
		baseURL: whisperCppModelBaseURL,
		logger:  logger,
	}
}

// Name reports the binary and model, e.g. "local:whisper-cli/base".
func (l *localWhisperCpp) Name() string {
	return "local:" + filepath.Base(l.bin) + "/" + filepath.Base(l.size)
}

// Transcribe ensures the model exists, then runs whisper-cli via the generic
// command backend (stdout transcript).
//
// whisper.cpp only decodes WAV/MP3/FLAC/OGG-Vorbis — NOT the OGG/Opus that
// Telegram and WhatsApp voice notes use. So when ffmpeg is available we first
// transcode to 16 kHz mono WAV (the format whisper.cpp wants), which makes Opus
// — and everything else — work. Without ffmpeg we hand the original to
// whisper-cli (fine for wav/mp3/flac) and, if it can't decode it, return an
// actionable error.
func (l *localWhisperCpp) Transcribe(ctx context.Context, audio []byte, mimeType, filename, language string) (string, error) {
	model, err := l.ensureModel(ctx)
	if err != nil {
		return "", err
	}

	data, mime, converted := audio, mimeType, false
	if ff := lookupFFmpeg(); ff != "" {
		if wav, cErr := ffmpegToWAV(ctx, ff, audio, mimeType); cErr == nil {
			data, mime, converted = wav, "audio/wav", true
		} else {
			l.logger.Warn("transcription: ffmpeg transcode failed; passing original to whisper-cli", zap.Error(cErr))
		}
	}

	// Build argv explicitly (not a space-split template): l.bin and model can be
	// absolute paths containing spaces (Windows "Program Files", user dirs), so
	// splitting on whitespace would corrupt the command.
	argv := []string{l.bin, "-m", model, "-nt", "-f", "{input}"}
	if strings.TrimSpace(language) != "" {
		argv = append(argv, "-l", "{lang}")
	}
	runner := newCommandFromArgv(argv, "local")
	out, err := runner.Transcribe(ctx, data, mime, filename, language)
	if err != nil && !converted && strings.Contains(err.Error(), "no transcript") {
		return "", fmt.Errorf("whisper.cpp produced no transcript — it cannot decode Opus voice notes (Telegram/WhatsApp). Install ffmpeg (e.g. brew install ffmpeg) for local Opus support, or use a cloud backend: %w", err)
	}
	return out, err
}

// ffmpegToWAV transcodes the audio to 16 kHz mono WAV — the format whisper.cpp
// decodes — handling Opus and anything else ffmpeg supports.
func ffmpegToWAV(ctx context.Context, ffmpeg string, audio []byte, mimeType string) ([]byte, error) {
	in, err := os.CreateTemp("", "chatcli-stt-in-*"+extensionForMime(mimeType))
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(in.Name()) }()
	if _, err := in.Write(audio); err != nil {
		_ = in.Close()
		return nil, err
	}
	if err := in.Close(); err != nil {
		return nil, err
	}
	outPath := in.Name() + ".wav"
	defer func() { _ = os.Remove(outPath) }()

	// #nosec G204 -- ffmpeg path comes from exec.LookPath; all args are fixed.
	cmd := exec.CommandContext(ctx, ffmpeg, "-nostdin", "-y", "-i", in.Name(), "-ar", "16000", "-ac", "1", "-f", "wav", outPath)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %w: %s", err, snippet([]byte(stderr.String())))
	}
	return os.ReadFile(outPath) // #nosec G304 -- path is our own temp file
}

// ensureModel resolves (and, if needed, downloads) the model exactly once.
func (l *localWhisperCpp) ensureModel(ctx context.Context) (string, error) {
	l.once.Do(func() { l.model, l.modelErr = l.resolveModel(ctx) })
	return l.model, l.modelErr
}

func (l *localWhisperCpp) resolveModel(ctx context.Context) (string, error) {
	// Explicit model file path.
	if strings.Contains(l.size, "/") || strings.HasSuffix(l.size, ".bin") {
		if fileExists(l.size) {
			return l.size, nil
		}
		return "", fmt.Errorf("transcription: model file not found: %s", l.size)
	}

	dir := l.cacheDir
	if dir == "" {
		var err error
		if dir, err = modelCacheDir(); err != nil {
			return "", err
		}
	}
	path := filepath.Join(dir, "ggml-"+l.size+".bin")
	if fi, statErr := os.Stat(path); statErr == nil && fi.Size() >= minModelBytes {
		return path, nil
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("transcription: model cache dir: %w", err)
	}
	l.logger.Info("transcription: downloading whisper.cpp model (one-time, may take a minute)",
		zap.String("size", l.size), zap.String("dest", path))
	if err := downloadModel(ctx, l.baseURL+"/ggml-"+l.size+".bin", path); err != nil {
		return "", fmt.Errorf("transcription: model download failed: %w", err)
	}
	return path, nil
}

// chooseWhisperCppModel picks the model: WHISPER_MODEL (path) wins, then an
// explicit path or known size in `model`, else the default size.
func chooseWhisperCppModel(model string) string {
	if p := strings.TrimSpace(os.Getenv("WHISPER_MODEL")); p != "" {
		return p
	}
	model = strings.TrimSpace(model)
	if strings.Contains(model, "/") || strings.HasSuffix(model, ".bin") {
		return model
	}
	if isWhisperSize(model) {
		return model
	}
	return defaultWhisperCppSize
}

// isWhisperSize reports whether s is a recognized whisper model size keyword.
func isWhisperSize(s string) bool {
	switch s {
	case "tiny", "tiny.en", "base", "base.en", "small", "small.en",
		"medium", "medium.en", "large-v1", "large-v2", "large-v3", "large":
		return true
	}
	return false
}

// modelCacheDir returns ~/.cache/chatcli/whisper (or the OS cache equivalent).
func modelCacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "chatcli", "whisper"), nil
}

// downloadModel fetches url to dest atomically, rejecting tiny (error-page) bodies.
func downloadModel(ctx context.Context, url, dest string) error {
	client := utils.NewHTTPClient(zap.NewNop(), modelDownloadTimeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download status %d", resp.StatusCode)
	}

	tmp := dest + ".part"
	out, err := os.Create(tmp) // #nosec G304 -- dest is inside our own cache dir
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(out, resp.Body)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	if n < minModelBytes {
		_ = os.Remove(tmp)
		return fmt.Errorf("downloaded model too small (%d bytes) — likely an error response", n)
	}
	return os.Rename(tmp, dest)
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// lookupFFmpeg returns the ffmpeg binary path, or "" when not installed.
// Indirected so tests can exercise the transcode path without a real ffmpeg.
var lookupFFmpeg = func() string {
	p, _ := exec.LookPath("ffmpeg")
	return p
}
