/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * Engine selection for surfaces that prefer the embedded voice.
 *
 * NewFromEnv is the process default (gateway voice replies, @speak,
 * /config), where the embedded Kokoro engine serves only from cache. A
 * surface that can ask the user before a download (the web UI) may prefer
 * the embedded voice over a system or cloud one instead: that is what
 * SelectPreferEmbedded resolves. An explicit environment choice always
 * wins, and the result never carries an embedded engine that still has to
 * download, so a first click never turns into a silent 150MB fetch.
 */
package tts

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/diillson/chatcli/llm/internal/provision"
	"go.uber.org/zap"
)

// Selection sources.
const (
	// SourceEnv: the environment chose the backend.
	SourceEnv = "env"
	// SourceEmbedded: the installed embedded engine.
	SourceEmbedded = "embedded"
	// SourceAuto: the download-free automatic chain, used while the
	// embedded engine is not installed.
	SourceAuto = "auto"
	// SourceNone: nothing usable without a download.
	SourceNone = "none"
)

// kokoroArchiveBytes is the published size of the Kokoro model tarball.
const kokoroArchiveBytes = 132_303_094

// Provisioner is implemented by providers that download an engine or model
// on first use; EnsureReady performs that one-time download up front.
type Provisioner interface {
	EnsureReady(ctx context.Context) error
}

// EnsureReady provisions the engine and model if the cache is missing them.
func (e *embeddedSynth) EnsureReady(ctx context.Context) error {
	_, err := e.ensureProvisioned(ctx)
	return err
}

// EmbeddedInfo describes the embedded Kokoro engine on this machine. It is
// computed from the cache alone and never touches the network.
type EmbeddedInfo struct {
	// Name is the provider name, e.g. "embedded:kokoro/bm_george".
	Name string
	// Supported reports that a prebuilt engine exists for this platform.
	Supported bool
	// Installed reports a complete install in the cache.
	Installed bool
	// DownloadBytes approximates what the first use still downloads
	// (0 once installed; pieces already in the cache are not counted).
	DownloadBytes int64
}

// Selection is the engine a surface that prefers the embedded voice uses.
type Selection struct {
	// Active synthesizes now without any download; Null when nothing is
	// usable yet. It is never an embedded engine that is not installed.
	Active Provider
	// Source is one of the Source* constants.
	Source string
	// Embedded describes the embedded engine on this machine.
	Embedded EmbeddedInfo
	// PreferEmbedded reports that the environment chose no backend, so the
	// surface should offer the embedded install over Active.
	PreferEmbedded bool
	// NewEmbedded builds a fresh embedded provider for an install; nil when
	// the platform has no prebuilt engine.
	NewEmbedded func() Provider
}

// Explicit reports whether the environment names a speech backend:
// CHATCLI_TTS_PROVIDER other than auto, CHATCLI_TTS_CMD or CHATCLI_TTS_URL.
// A cloud API key alone is not a choice of voice; it is there for the chat
// models.
func Explicit() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CHATCLI_TTS_PROVIDER"))) {
	case "", "auto":
	default:
		return true
	}
	return strings.TrimSpace(os.Getenv("CHATCLI_TTS_CMD")) != "" ||
		strings.TrimSpace(os.Getenv("CHATCLI_TTS_URL")) != ""
}

// NewEmbeddedFromEnv builds the embedded Kokoro provider with the voices
// CHATCLI_TTS_VOICE and CHATCLI_TTS_VOICE_PT name. Nothing is downloaded
// until the provider is first used or provisioned.
func NewEmbeddedFromEnv(logger *zap.Logger) Provider { return embeddedFromEnv(logger) }

// DescribeEmbedded reports the embedded engine facts for p; ok is false
// when p is not the embedded engine.
func DescribeEmbedded(p Provider) (EmbeddedInfo, bool) {
	e, ok := p.(*embeddedSynth)
	if !ok {
		return EmbeddedInfo{}, false
	}
	return e.info(), true
}

// info computes the embedded facts from the cache.
func (e *embeddedSynth) info() EmbeddedInfo {
	in := EmbeddedInfo{Name: e.Name()}
	if _, ok := provision.SherpaAsset(runtime.GOOS, runtime.GOARCH); !ok {
		return in
	}
	in.Supported = true
	if e.isProvisioned() {
		in.Installed = true
		return in
	}
	root, err := e.root()
	if err != nil {
		return in
	}
	if !dirExistsTTS(filepath.Join(root, "sherpa-v"+sherpaVersion)) {
		in.DownloadBytes += provision.SherpaAssetBytes(runtime.GOOS, runtime.GOARCH)
	}
	if !dirExistsTTS(filepath.Join(root, "kokoro")) {
		in.DownloadBytes += kokoroArchiveBytes
	}
	return in
}

func dirExistsTTS(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// SelectPreferEmbedded resolves the voice for a surface that prefers the
// embedded engine: an explicit environment choice wins (a pinned embedded
// engine that is not installed yet waits for the install); otherwise the
// installed embedded engine; otherwise the download-free automatic chain,
// with the embedded install on offer.
func SelectPreferEmbedded(logger *zap.Logger) Selection {
	if logger == nil {
		logger = zap.NewNop()
	}
	emb := NewEmbedded(os.Getenv("CHATCLI_TTS_VOICE"), os.Getenv("CHATCLI_TTS_VOICE_PT"), logger)
	sel := Selection{Embedded: emb.info()}
	if sel.Embedded.Supported {
		sel.NewEmbedded = func() Provider { return embeddedFromEnv(logger) }
	}
	if Explicit() {
		sel.Source = SourceEnv
		sel.Active = withoutPendingDownload(NewFromEnv(logger))
		return sel
	}
	sel.PreferEmbedded = sel.Embedded.Supported
	if sel.Embedded.Installed {
		sel.Active, sel.Source = emb, SourceEmbedded
		return sel
	}
	sel.Active = withoutPendingDownload(NewFromEnv(logger))
	sel.Source = SourceAuto
	if IsNull(sel.Active) {
		sel.Source = SourceNone
	}
	return sel
}

// withoutPendingDownload replaces an embedded engine that would download on
// first use with Null, so the surface asks before fetching it.
func withoutPendingDownload(p Provider) Provider {
	if e, ok := p.(*embeddedSynth); ok && !e.isProvisioned() {
		return NewNull()
	}
	return p
}

// ProvisionProgress is one step of an embedded engine install: Phase is
// "download" (Done of Total bytes of File; Total -1 when unknown) or
// "extract".
type ProvisionProgress struct {
	Phase string
	File  string
	Done  int64
	Total int64
}

// WithProvisionProgress returns a context under which an embedded engine
// install reports each step to fn. fn must not block.
func WithProvisionProgress(ctx context.Context, fn func(ProvisionProgress)) context.Context {
	if fn == nil {
		return ctx
	}
	return provision.WithProgress(ctx, func(p provision.Progress) {
		fn(ProvisionProgress{Phase: p.Phase, File: p.File, Done: p.Done, Total: p.Total})
	})
}
