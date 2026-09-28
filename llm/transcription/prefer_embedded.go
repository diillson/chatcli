/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * Engine selection for surfaces that prefer the embedded engine.
 *
 * NewFromEnv is the process default (gateway, @speak, /config): local and
 * keyless first, with the embedded engine used only from cache or as the last
 * resort. A surface that can ask the user before a download (the web UI) may
 * prefer the embedded engine over the cloud fallbacks instead: that is what
 * SelectPreferEmbedded resolves. An explicit environment choice always wins,
 * and the result never carries an embedded engine that still has to
 * download, so a first click never turns into a silent 200MB fetch.
 */
package transcription

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

// whisperArchiveBytes are the published sizes of the whisper model
// tarballs, for telling the user how much the first use downloads.
var whisperArchiveBytes = map[string]int64{
	"tiny": 116_204_861, "base": 207_557_382, "small": 639_387_718,
	"medium": 1_931_372_882, "large-v3": 1_068_482_488,
	"tiny.en": 118_071_777, "base.en": 208_576_005, "small.en": 635_693_775,
	"medium.en": 1_905_872_689,
}

// EmbeddedInfo describes the embedded whisper engine on this machine. It is
// computed from the cache alone and never touches the network.
type EmbeddedInfo struct {
	// Name is the provider name, e.g. "embedded:whisper/base".
	Name string
	// Supported reports that a prebuilt engine exists for this platform.
	Supported bool
	// Installed reports a complete install in the cache.
	Installed bool
	// DownloadBytes approximates what the first use still downloads
	// (0 once installed; pieces already in the cache are not counted).
	DownloadBytes int64
}

// Selection is the engine a surface that prefers the embedded engine uses.
type Selection struct {
	// Active serves transcriptions now without any download; Null when
	// nothing is usable yet. It is never an embedded engine that is not
	// installed.
	Active Provider
	// Source is one of the Source* constants.
	Source string
	// Embedded describes the embedded engine on this machine.
	Embedded EmbeddedInfo
	// PreferEmbedded reports that the environment chose no backend, so the
	// surface should offer the embedded install over Active.
	PreferEmbedded bool
	// NewEmbedded builds a fresh embedded provider for an install; nil when
	// the platform has no prebuilt engine. A fresh instance per attempt
	// keeps a cancelled install from latching its error.
	NewEmbedded func() Provider
}

// Explicit reports whether the environment names a transcription backend:
// CHATCLI_TRANSCRIPTION_PROVIDER other than auto, CHATCLI_TRANSCRIPTION_CMD
// or CHATCLI_TRANSCRIPTION_URL. A cloud API key alone is not a choice of
// speech backend; it is there for the chat models.
func Explicit() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CHATCLI_TRANSCRIPTION_PROVIDER"))) {
	case "", "auto":
	default:
		return true
	}
	return strings.TrimSpace(os.Getenv("CHATCLI_TRANSCRIPTION_CMD")) != "" ||
		strings.TrimSpace(os.Getenv("CHATCLI_TRANSCRIPTION_URL")) != ""
}

// NewEmbeddedFromEnv builds the embedded whisper provider with the model
// size CHATCLI_TRANSCRIPTION_MODEL names (base by default). Nothing is
// downloaded until the provider is first used or provisioned.
func NewEmbeddedFromEnv(logger *zap.Logger) Provider {
	return NewEmbeddedWhisper(strings.TrimSpace(os.Getenv("CHATCLI_TRANSCRIPTION_MODEL")), logger)
}

// DescribeEmbedded reports the embedded engine facts for p; ok is false
// when p is not the embedded engine.
func DescribeEmbedded(p Provider) (EmbeddedInfo, bool) {
	e, ok := p.(*embeddedWhisper)
	if !ok {
		return EmbeddedInfo{}, false
	}
	return e.info(), true
}

// info computes the embedded facts from the cache.
func (e *embeddedWhisper) info() EmbeddedInfo {
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
	if !dirExists(filepath.Join(root, "sherpa-v"+provision.SherpaVersion)) {
		in.DownloadBytes += provision.SherpaAssetBytes(runtime.GOOS, runtime.GOARCH)
	}
	if !dirExists(filepath.Join(root, "whisper-"+e.size)) {
		in.DownloadBytes += whisperArchiveBytes[e.size]
	}
	return in
}

func dirExists(p string) bool {
	fi, err := os.Stat(filepath.Clean(p)) // #nosec G703 -- existence check only, under the operator-configured cache dir (validated absolute)
	return err == nil && fi.IsDir()
}

// SelectPreferEmbedded resolves the backend for a surface that prefers the
// embedded engine: an explicit environment choice wins (a pinned embedded
// engine that is not installed yet waits for the install); otherwise the
// installed embedded engine; otherwise the download-free automatic chain,
// with the embedded install on offer.
func SelectPreferEmbedded(logger *zap.Logger) Selection {
	if logger == nil {
		logger = zap.NewNop()
	}
	emb := NewEmbeddedWhisper(strings.TrimSpace(os.Getenv("CHATCLI_TRANSCRIPTION_MODEL")), logger)
	sel := Selection{Embedded: emb.info()}
	if sel.Embedded.Supported {
		sel.NewEmbedded = func() Provider { return NewEmbeddedFromEnv(logger) }
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
	if e, ok := p.(*embeddedWhisper); ok && !e.isProvisioned() {
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
