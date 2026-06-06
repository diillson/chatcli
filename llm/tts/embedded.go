/*
 * ChatCLI - Embedded TTS provider (Kokoro via sherpa-onnx).
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Keyless, serverless and OS-agnostic voice output: on first synthesis the
 * provider downloads a prebuilt sherpa-onnx-offline-tts CLI plus the Kokoro
 * multi-lang model into the user cache (one-time, ~150MB) and shells out per
 * clip — no cgo, no API key, no companion server, works the same on Linux,
 * macOS and Windows. Replies route to a Portuguese or English voice by
 * detected language, so a mixed conversation answers each message in its own
 * language with the right accent.
 */
package tts

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// embeddedSynthTimeout bounds one clip's synthesis; generation runs at several
// times real-time, so a healthy run finishes in seconds.
const embeddedSynthTimeout = 2 * time.Minute

// embeddedSynth is the self-provisioning Kokoro provider.
type embeddedSynth struct {
	enVoice string // voice for English (and unrecognized) text
	ptVoice string // voice for Portuguese text
	logger  *zap.Logger

	// Test overrides; empty/zero means production defaults.
	cacheDir      string
	binBaseURL    string
	modelURL      string
	minBinBytes   int64
	minModelBytes int64

	once    sync.Once
	paths   provisionedPaths
	provErr error

	warnVoiceOnce  sync.Once
	warnFFmpegOnce sync.Once
}

// NewEmbedded builds the provider. Empty voice names take the Jarvis-style
// defaults; unknown names degrade to the defaults on first use with a warning
// rather than failing every synthesis.
func NewEmbedded(enVoice, ptVoice string, logger *zap.Logger) *embeddedSynth {
	if logger == nil {
		logger = zap.NewNop()
	}
	if strings.TrimSpace(enVoice) == "" {
		enVoice = defaultEmbeddedEnVoice
	}
	if strings.TrimSpace(ptVoice) == "" {
		ptVoice = defaultEmbeddedPtVoice
	}
	return &embeddedSynth{
		enVoice:       enVoice,
		ptVoice:       ptVoice,
		logger:        logger,
		binBaseURL:    sherpaReleaseBase,
		modelURL:      kokoroModelURL,
		minBinBytes:   minSherpaBytes,
		minModelBytes: minKokoroBytes,
	}
}

// Name reports the backend and its English voice, e.g. "embedded:kokoro/bm_george".
func (e *embeddedSynth) Name() string { return "embedded:kokoro/" + e.enVoice }

// root returns the cache root, honoring the test override.
func (e *embeddedSynth) root() (string, error) {
	if e.cacheDir != "" {
		return e.cacheDir, nil
	}
	return embeddedCacheDir()
}

// isProvisioned reports whether the cache already holds a complete install.
// It never touches the network — the factory uses it to decide if embedded
// may serve in auto mode without surprising the user with a 150MB download.
func (e *embeddedSynth) isProvisioned() bool {
	root, err := e.root()
	if err != nil {
		return false
	}
	return isProvisionedDir(root)
}

// Synthesize speaks text, provisioning the engine on first use. An explicit
// voice wins; otherwise the reply routes to the Portuguese or English voice by
// detected language. format ogg/opus delivers a Telegram-ready voice note when
// ffmpeg is available, degrading to WAV otherwise.
func (e *embeddedSynth) Synthesize(ctx context.Context, text, voice, format string) (Audio, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Audio{}, fmt.Errorf("tts: empty text")
	}
	paths, err := e.ensureProvisioned(ctx)
	if err != nil {
		return Audio{}, err
	}
	wav, err := e.runKokoro(ctx, paths, e.resolveVoice(text, voice), text)
	if err != nil {
		return Audio{}, err
	}
	return e.deliver(ctx, wav, format)
}

// resolveVoice picks the speaker: an explicit, known voice wins; unknown
// explicit names warn once and fall back to language routing.
func (e *embeddedSynth) resolveVoice(text, explicit string) string {
	if v := strings.TrimSpace(explicit); v != "" {
		if _, ok := voiceInfo(v); ok {
			return v
		}
		e.warnVoiceOnce.Do(func() {
			e.logger.Warn("tts: unknown kokoro voice; using language routing",
				zap.String("voice", explicit))
		})
	}
	routed := pickVoice(text, e.enVoice, e.ptVoice)
	if _, ok := voiceInfo(routed); !ok {
		e.warnVoiceOnce.Do(func() {
			e.logger.Warn("tts: configured voice unknown; using default",
				zap.String("voice", routed), zap.String("default", defaultEmbeddedEnVoice))
		})
		return defaultEmbeddedEnVoice
	}
	return routed
}

// ensureProvisioned resolves (and, if needed, downloads) the engine exactly
// once per provider instance; the on-disk marker makes it once per machine.
func (e *embeddedSynth) ensureProvisioned(ctx context.Context) (provisionedPaths, error) {
	e.once.Do(func() { e.paths, e.provErr = e.provision(ctx) })
	return e.paths, e.provErr
}

// provision downloads and extracts whatever the cache is missing, then writes
// the version marker. Each archive lands atomically, so a previously
// completed piece is never re-downloaded after an interrupted run.
func (e *embeddedSynth) provision(ctx context.Context) (provisionedPaths, error) {
	root, err := e.root()
	if err != nil {
		return provisionedPaths{}, fmt.Errorf("tts: resolve cache dir: %w", err)
	}
	if isProvisionedDir(root) {
		p, _ := locateProvisioned(root)
		return p, nil
	}
	asset, ok := sherpaAsset(runtime.GOOS, runtime.GOARCH)
	if !ok {
		return provisionedPaths{}, fmt.Errorf(
			"tts: embedded voice has no prebuilt engine for %s/%s — set CHATCLI_TTS_CMD, CHATCLI_TTS_URL or a cloud key",
			runtime.GOOS, runtime.GOARCH)
	}
	if err := os.MkdirAll(root, 0o750); err != nil {
		return provisionedPaths{}, fmt.Errorf("tts: create cache dir: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, provisionTimeout)
	defer cancel()

	binDir := filepath.Join(root, "sherpa-v"+sherpaVersion)
	modelDir := filepath.Join(root, "kokoro")
	if err := e.provisionPiece(ctx, e.binBaseURL+asset, binDir, e.minBinBytes, "engine (~25MB)"); err != nil {
		return provisionedPaths{}, err
	}
	if err := e.provisionPiece(ctx, e.modelURL, modelDir, e.minModelBytes, "voice model (~126MB)"); err != nil {
		return provisionedPaths{}, err
	}

	p, ok := locateProvisioned(root)
	if !ok {
		return provisionedPaths{}, fmt.Errorf("tts: provisioned cache is incomplete — remove %s and retry", root)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(p.bin, 0o755); err != nil {
			return provisionedPaths{}, fmt.Errorf("tts: mark engine executable: %w", err)
		}
	}
	if err := os.WriteFile(readyMarker(root), nil, 0o600); err != nil {
		return provisionedPaths{}, fmt.Errorf("tts: write ready marker: %w", err)
	}
	e.logger.Info("tts: embedded voice provisioned", zap.String("cache", root))
	return p, nil
}

// provisionPiece downloads and extracts one archive unless its directory is
// already in place from a previous (atomic) run.
func (e *embeddedSynth) provisionPiece(ctx context.Context, url, targetDir string, minBytes int64, what string) error {
	if fi, err := os.Stat(targetDir); err == nil && fi.IsDir() {
		return nil
	}
	e.logger.Info("tts: downloading embedded voice "+what+" (one-time)", zap.String("url", url))
	if err := provisionArchive(ctx, url, targetDir, minBytes); err != nil {
		return fmt.Errorf("tts: provision %s: %w", what, err)
	}
	return nil
}

// runKokoro shells out to sherpa-onnx-offline-tts and returns the WAV bytes.
func (e *embeddedSynth) runKokoro(ctx context.Context, paths provisionedPaths, voice, text string) ([]byte, error) {
	info, _ := voiceInfo(voice) // resolveVoice guarantees a known voice

	out, err := os.CreateTemp("", "chatcli-tts-*.wav")
	if err != nil {
		return nil, fmt.Errorf("tts: temp file: %w", err)
	}
	outPath := out.Name()
	_ = out.Close()
	defer func() { _ = os.Remove(outPath) }()

	ctx, cancel := context.WithTimeout(ctx, embeddedSynthTimeout)
	defer cancel()

	args := []string{
		"--kokoro-model=" + paths.model,
		"--kokoro-voices=" + paths.voices,
		"--kokoro-tokens=" + paths.tokens,
		"--kokoro-data-dir=" + paths.dataDir,
		"--kokoro-lang=" + info.lang,
		"--num-threads=" + strconv.Itoa(synthThreads()),
		"--sid=" + strconv.Itoa(info.sid),
		"--output-filename=" + outPath,
		text,
	}
	cmd := exec.CommandContext(ctx, paths.bin, args...) // #nosec G204 -- binary and args come from our own cache
	cmd.Env = libPathEnv(paths.libDir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return nil, fmt.Errorf("tts: kokoro synthesis failed: %w: %s", err, lastLine(msg))
		}
		return nil, fmt.Errorf("tts: kokoro synthesis failed: %w", err)
	}
	data, err := os.ReadFile(outPath) // #nosec G304 -- temp file we created
	if err != nil {
		return nil, fmt.Errorf("tts: read output: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("tts: kokoro produced no audio")
	}
	return data, nil
}

// deliver converts the WAV to the requested format when possible. ogg/opus
// uses ffmpeg; anything else (or no ffmpeg) returns the WAV unchanged so the
// reply always goes out.
func (e *embeddedSynth) deliver(ctx context.Context, wav []byte, format string) (Audio, error) {
	f := strings.ToLower(strings.TrimSpace(format))
	if f == "ogg" || f == "opus" {
		ffmpeg := lookupFFmpegTTS()
		if ffmpeg == "" {
			e.warnFFmpegOnce.Do(func() {
				e.logger.Warn("tts: ffmpeg not found; voice notes degrade to wav audio files")
			})
		} else if ogg, err := wavToOpus(ctx, ffmpeg, wav); err == nil {
			mime, ext := mimeFor("ogg")
			return Audio{Data: ogg, Mime: mime, Ext: ext}, nil
		} else {
			e.logger.Warn("tts: opus transcode failed; sending wav", zap.Error(err))
		}
	}
	mime, ext := mimeFor("wav")
	return Audio{Data: wav, Mime: mime, Ext: ext}, nil
}

// synthThreads sizes the engine's thread pool: half the cores, clamped to
// [1,4] — synthesis is short-lived and must not starve the host.
func synthThreads() int {
	n := runtime.NumCPU() / 2
	if n < 1 {
		return 1
	}
	if n > 4 {
		return 4
	}
	return n
}

// libPathEnv returns the process environment with the engine's shared-library
// directory prepended to the platform's loader path. The upstream binaries
// carry an rpath, so this is a belt-and-suspenders for stripped environments.
func libPathEnv(libDir string) []string {
	var name string
	switch runtime.GOOS {
	case "linux":
		name = "LD_LIBRARY_PATH"
	case "darwin":
		name = "DYLD_LIBRARY_PATH"
	case "windows":
		name = "PATH"
	default:
		return os.Environ()
	}
	env := os.Environ()
	sep := string(os.PathListSeparator)
	for i, kv := range env {
		if strings.HasPrefix(kv, name+"=") {
			env[i] = name + "=" + libDir + sep + kv[len(name)+1:]
			return env
		}
	}
	return append(env, name+"="+libDir)
}
