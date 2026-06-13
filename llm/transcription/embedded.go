/*
 * ChatCLI - Embedded STT provider (Whisper via sherpa-onnx).
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Keyless, serverless and OS-agnostic voice input: on first transcription the
 * provider downloads the prebuilt sherpa-onnx CLI bundle (the same engine the
 * embedded TTS uses — its tarball ships `sherpa-onnx-offline` for recognition
 * next to the TTS binary) plus a multilingual Whisper ONNX model into the user
 * cache and shells out per clip — no cgo, no API key, no companion server,
 * works the same on Linux, macOS and Windows. The multilingual model
 * auto-detects the spoken language, so Portuguese and English voice notes both
 * transcribe correctly with zero configuration.
 */
package transcription

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/llm/internal/audio"
	"github.com/diillson/chatcli/llm/internal/provision"
	"go.uber.org/zap"
)

const (
	// whisperAssetBase hosts the Whisper ONNX exports that track the pinned
	// sherpa-onnx release (provision.SherpaVersion); bump them together.
	whisperAssetBase = "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/"

	// asrBinName is the recognition executable inside the engine tarball.
	asrBinName = "sherpa-onnx-offline"

	// defaultEmbeddedWhisperSize balances pt-BR/multilingual accuracy against
	// download size (~198MB one-time); "tiny" (~111MB) is the lighter option.
	defaultEmbeddedWhisperSize = "base"

	// Size floors reject HTML error pages served with status 200. The engine
	// tarball is ~25MB; the smallest whisper tarball (tiny) is ~111MB.
	minEngineBytes       = 5 << 20
	minWhisperModelBytes = 20 << 20

	// embeddedRunTimeout bounds one clip's recognition; voice notes are short
	// and whisper runs faster than real-time on CPU.
	embeddedRunTimeout = 5 * time.Minute
)

// sttPaths locates every artifact Transcribe needs after the cache is
// provisioned.
type sttPaths struct {
	bin     string // sherpa-onnx-offline executable
	libDir  string // shared libraries shipped next to the binary
	encoder string // <size>-encoder[.int8].onnx
	decoder string // <size>-decoder[.int8].onnx
	tokens  string // <size>-tokens.txt
}

// embeddedWhisper is the self-provisioning Whisper provider.
type embeddedWhisper struct {
	size   string // whisper model size (tiny|base|small|medium|large-v3|*.en)
	logger *zap.Logger

	// Test overrides; empty/zero means production defaults.
	cacheDir      string
	binBaseURL    string
	modelBaseURL  string
	minBinBytes   int64
	minModelBytes int64

	once    sync.Once
	paths   sttPaths
	provErr error
}

// NewEmbeddedWhisper builds the provider. model is the raw
// CHATCLI_TRANSCRIPTION_MODEL value; unknown values (cloud model names, ggml
// paths) take the default size rather than failing.
func NewEmbeddedWhisper(model string, logger *zap.Logger) *embeddedWhisper {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &embeddedWhisper{
		size:          chooseEmbeddedWhisperSize(model),
		logger:        logger,
		binBaseURL:    provision.SherpaReleaseBase,
		modelBaseURL:  whisperAssetBase,
		minBinBytes:   minEngineBytes,
		minModelBytes: minWhisperModelBytes,
	}
}

// chooseEmbeddedWhisperSize maps the model env value to a published sherpa
// whisper asset, defaulting to the multilingual base model.
func chooseEmbeddedWhisperSize(model string) string {
	switch s := strings.TrimSpace(strings.ToLower(model)); s {
	case "tiny", "base", "small", "medium", "large-v3",
		"tiny.en", "base.en", "small.en", "medium.en":
		return s
	}
	return defaultEmbeddedWhisperSize
}

// Name reports the backend and model, e.g. "embedded:whisper/base".
func (e *embeddedWhisper) Name() string { return "embedded:whisper/" + e.size }

// EnsureReady provisions the engine and model if the cache is missing them —
// the gateway calls it in the background at startup so the one-time download
// happens before the first voice note arrives, not during it.
func (e *embeddedWhisper) EnsureReady(ctx context.Context) error {
	_, err := e.ensureProvisioned(ctx)
	return err
}

// root returns the cache root, honoring the test override.
func (e *embeddedWhisper) root() (string, error) {
	if e.cacheDir != "" {
		return e.cacheDir, nil
	}
	return sttCacheDir()
}

// sttCacheDir returns ~/.cache/chatcli/stt (or the OS cache equivalent),
// sibling of the tts and whisper.cpp caches. CHATCLI_TRANSCRIPTION_CACHE_DIR
// relocates it — useful for shared caches and air-gapped pre-seeding.
func sttCacheDir() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("CHATCLI_TRANSCRIPTION_CACHE_DIR")); dir != "" {
		// Operator-supplied override: canonicalize and require an absolute
		// path so a relative value can never resolve against an unexpected
		// working directory (the daemon and the REPL have different cwds).
		dir = filepath.Clean(dir)
		if !filepath.IsAbs(dir) {
			return "", fmt.Errorf("transcription: CHATCLI_TRANSCRIPTION_CACHE_DIR must be an absolute path, got %q", dir)
		}
		return dir, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "chatcli", "stt"), nil
}

// sttReadyMarker certifies a complete provision of the pinned engine version
// plus one model size; different sizes coexist under the same root.
func sttReadyMarker(root, size string) string {
	return filepath.Join(root, ".ready-v"+provision.SherpaVersion+"-"+size)
}

// isProvisioned reports whether the cache already holds a complete install.
// It never touches the network — the factory uses it to decide if embedded
// may serve in auto mode without surprising the user with a large download.
func (e *embeddedWhisper) isProvisioned() bool {
	root, err := e.root()
	if err != nil {
		return false
	}
	if !fileExists(sttReadyMarker(root, e.size)) {
		return false
	}
	_, ok := locateSTT(root, e.size)
	return ok
}

// locateSTT walks the cache root and resolves every artifact path. The walk
// tolerates upstream layout changes by searching for well-known names instead
// of assuming a directory shape, and runs inside an os.Root so traversal is
// kernel-confined to the cache directory.
func locateSTT(root, size string) (sttPaths, bool) {
	confined, err := os.OpenRoot(root)
	if err != nil {
		return sttPaths{}, false
	}
	defer func() { _ = confined.Close() }()

	binName := asrBinName
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	modelSubtree := "whisper-" + size

	var p sttPaths
	var encI8, decI8, encF32, decF32 string
	_ = fs.WalkDir(confined.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil // unreadable entry: keep scanning the rest
		}
		abs := filepath.Join(root, filepath.FromSlash(rel))
		name := d.Name()
		if name == binName && p.bin == "" {
			p.bin = abs
			return nil
		}
		// Model artifacts only count inside this size's subtree, so caches
		// holding several sizes never cross-wire encoder and tokens files.
		if !strings.HasPrefix(rel, modelSubtree+"/") {
			return nil
		}
		switch {
		case strings.HasSuffix(name, "-encoder.int8.onnx"):
			encI8 = abs
		case strings.HasSuffix(name, "-decoder.int8.onnx"):
			decI8 = abs
		case strings.HasSuffix(name, "-encoder.onnx"):
			encF32 = abs
		case strings.HasSuffix(name, "-decoder.onnx"):
			decF32 = abs
		case strings.HasSuffix(name, "-tokens.txt"):
			p.tokens = abs
		}
		return nil
	})
	// Prefer the int8 pair (4x smaller working set, ~same accuracy); fall back
	// to fp32 only when the quantized pair is incomplete.
	p.encoder, p.decoder = encI8, decI8
	if p.encoder == "" || p.decoder == "" {
		p.encoder, p.decoder = encF32, decF32
	}
	if p.bin == "" || p.encoder == "" || p.decoder == "" || p.tokens == "" {
		return sttPaths{}, false
	}
	p.libDir = provision.FindLibDir(p.bin)
	return p, true
}

// Transcribe recognizes the audio, provisioning the engine on first use.
// sherpa-onnx decodes WAV only, so anything else (the OGG/Opus of Telegram
// and WhatsApp voice notes, mp3, …) is transcoded via ffmpeg first.
func (e *embeddedWhisper) Transcribe(ctx context.Context, audio []byte, mimeType, filename, language string) (string, error) {
	paths, err := e.ensureProvisioned(ctx)
	if err != nil {
		return "", err
	}
	wav, err := e.toWAV(ctx, audio, mimeType, filename)
	if err != nil {
		return "", err
	}
	return e.run(ctx, paths, wav, language)
}

// engineSampleRate is the PCM rate the sherpa-onnx whisper engine expects.
const engineSampleRate = 16000

// toWAV converts the clip to the 16kHz mono WAV the engine wants, walking a
// graceful-degradation chain: ffmpeg when installed (the reference decoder,
// covers every format), the pure-Go Opus decoder for the OGG/Opus voice notes
// messaging platforms send (so the zero-config install needs no ffmpeg), WAV
// passthrough, and finally an actionable per-platform error.
func (e *embeddedWhisper) toWAV(ctx context.Context, clip []byte, mimeType, filename string) ([]byte, error) {
	if ff := lookupFFmpeg(); ff != "" {
		wav, err := ffmpegToWAV(ctx, ff, clip, mimeType)
		if err == nil {
			return wav, nil
		}
		e.logger.Warn("transcription: ffmpeg transcode failed; trying built-in decoders", zap.Error(err))
	}
	wav, err := audio.DecodeOggOpusToWAV(ctx, clip, engineSampleRate)
	if err == nil {
		e.logger.Info("transcription: voice note decoded with the pure-Go opus decoder")
		return wav, nil
	}
	if !errors.Is(err, audio.ErrNotOggOpus) {
		e.logger.Warn("transcription: pure-Go opus decode failed", zap.Error(err))
	}
	if looksLikeWAV(clip, mimeType, filename) {
		return clip, nil
	}
	return nil, fmt.Errorf("transcription: no decoder for %s on this machine — %s: %w",
		describeClip(mimeType, filename), FFmpegInstallHint(), ErrNeedsFFmpeg)
}

// describeClip names the clip for error messages, preferring the MIME type.
func describeClip(mimeType, filename string) string {
	if m := strings.TrimSpace(mimeType); m != "" {
		return m
	}
	if f := strings.TrimSpace(filename); f != "" {
		return f
	}
	return "this audio format"
}

// looksLikeWAV reports whether the clip is plausibly RIFF/WAV already.
func looksLikeWAV(audio []byte, mimeType, filename string) bool {
	if bytes.HasPrefix(audio, []byte("RIFF")) {
		return true
	}
	if strings.Contains(strings.ToLower(mimeType), "wav") {
		return true
	}
	return strings.HasSuffix(strings.ToLower(filename), ".wav")
}

// run shells out to sherpa-onnx-offline and extracts the transcript from its
// JSON result line.
func (e *embeddedWhisper) run(ctx context.Context, paths sttPaths, wav []byte, language string) (string, error) {
	in, err := os.CreateTemp("", "chatcli-stt-*.wav")
	if err != nil {
		return "", fmt.Errorf("transcription: temp file: %w", err)
	}
	inPath := in.Name()
	defer func() { _ = os.Remove(inPath) }()
	if _, err := in.Write(wav); err != nil {
		_ = in.Close()
		return "", fmt.Errorf("transcription: write temp wav: %w", err)
	}
	if err := in.Close(); err != nil {
		return "", fmt.Errorf("transcription: close temp wav: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, embeddedRunTimeout)
	defer cancel()

	args := []string{
		"--tokens=" + paths.tokens,
		"--whisper-encoder=" + paths.encoder,
		"--whisper-decoder=" + paths.decoder,
		"--whisper-task=transcribe",
		"--num-threads=" + strconv.Itoa(recognitionThreads()),
	}
	// An explicit language pins recognition; empty (or "auto") lets the
	// multilingual model detect the spoken language — which is what makes
	// replies mirror the user's language.
	if lang := strings.TrimSpace(strings.ToLower(language)); lang != "" && lang != "auto" {
		args = append(args, "--whisper-language="+lang)
	}
	args = append(args, inPath)

	cmd := exec.CommandContext(ctx, paths.bin, args...) // #nosec G204 -- binary and args come from our own cache
	cmd.Env = provision.LibPathEnv(paths.libDir)
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		return "", fmt.Errorf("transcription: embedded whisper failed: %w: %s", runErr, snippet(out))
	}
	text, ok := parseSherpaResult(out)
	if !ok {
		return "", fmt.Errorf("transcription: embedded whisper produced no result: %s", snippet(out))
	}
	return strings.TrimSpace(text), nil
}

// parseSherpaResult extracts the "text" field from the engine output, which
// interleaves log lines with one JSON result object per input file.
func parseSherpaResult(out []byte) (string, bool) {
	var text string
	var found bool
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var res struct {
			Text *string `json:"text"`
		}
		if err := json.Unmarshal([]byte(line), &res); err == nil && res.Text != nil {
			text, found = *res.Text, true
		}
	}
	return text, found
}

// ensureProvisioned resolves (and, if needed, downloads) the engine exactly
// once per provider instance; the on-disk marker makes it once per machine.
func (e *embeddedWhisper) ensureProvisioned(ctx context.Context) (sttPaths, error) {
	e.once.Do(func() { e.paths, e.provErr = e.provision(ctx) })
	return e.paths, e.provErr
}

// provision downloads and extracts whatever the cache is missing, then writes
// the version marker. Each archive lands atomically, so a previously
// completed piece is never re-downloaded after an interrupted run.
func (e *embeddedWhisper) provision(ctx context.Context) (sttPaths, error) {
	root, err := e.root()
	if err != nil {
		return sttPaths{}, fmt.Errorf("transcription: resolve cache dir: %w", err)
	}
	if fileExists(sttReadyMarker(root, e.size)) {
		if p, ok := locateSTT(root, e.size); ok {
			return p, nil
		}
	}
	asset, ok := provision.SherpaAsset(runtime.GOOS, runtime.GOARCH)
	if !ok {
		return sttPaths{}, fmt.Errorf(
			"transcription: embedded whisper has no prebuilt engine for %s/%s — install a whisper CLI, or set CHATCLI_TRANSCRIPTION_URL or a cloud key",
			runtime.GOOS, runtime.GOARCH)
	}
	if err := os.MkdirAll(root, 0o750); err != nil {
		return sttPaths{}, fmt.Errorf("transcription: create cache dir: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, provision.Timeout)
	defer cancel()

	engineDir := filepath.Join(root, "sherpa-v"+provision.SherpaVersion)
	modelDir := filepath.Join(root, "whisper-"+e.size)
	if err := e.provisionPiece(ctx, e.binBaseURL+asset, engineDir, e.minBinBytes, "engine (~25MB)"); err != nil {
		return sttPaths{}, err
	}
	modelURL := e.modelBaseURL + "sherpa-onnx-whisper-" + e.size + ".tar.bz2"
	if err := e.provisionPiece(ctx, modelURL, modelDir, e.minModelBytes, "whisper model ("+e.size+")"); err != nil {
		return sttPaths{}, err
	}

	p, ok := locateSTT(root, e.size)
	if !ok {
		return sttPaths{}, fmt.Errorf("transcription: provisioned cache is incomplete — remove %s and retry", root)
	}
	// The tar extractor preserves the executable bit, so the engine comes out
	// of the archive ready to run — verify rather than mutate, and fail loud
	// on a broken extraction instead of papering over it with a chmod.
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(p.bin)
		if err != nil || fi.Mode()&0o111 == 0 {
			return sttPaths{}, fmt.Errorf("transcription: provisioned engine is not executable (%s) — remove %s and retry", p.bin, root)
		}
	}
	if err := os.WriteFile(sttReadyMarker(root, e.size), nil, 0o600); err != nil {
		return sttPaths{}, fmt.Errorf("transcription: write ready marker: %w", err)
	}
	e.logger.Info("transcription: embedded whisper provisioned",
		zap.String("cache", root), zap.String("model", e.size))
	return p, nil
}

// provisionPiece downloads and extracts one archive unless its directory is
// already in place from a previous (atomic) run.
func (e *embeddedWhisper) provisionPiece(ctx context.Context, url, targetDir string, minBytes int64, what string) error {
	if fi, err := os.Stat(targetDir); err == nil && fi.IsDir() {
		return nil
	}
	e.logger.Info("transcription: downloading embedded "+what+" (one-time)", zap.String("url", url))
	if err := provision.ProvisionArchive(ctx, url, targetDir, minBytes); err != nil {
		return fmt.Errorf("transcription: provision %s: %w", what, err)
	}
	return nil
}

// recognitionThreads sizes the engine's thread pool: half the cores, clamped
// to [1,4] — recognition is short-lived and must not starve the host.
func recognitionThreads() int {
	n := runtime.NumCPU() / 2
	if n < 1 {
		return 1
	}
	if n > 4 {
		return 4
	}
	return n
}
