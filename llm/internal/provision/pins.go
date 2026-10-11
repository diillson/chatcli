/*
 * ChatCLI - Shared self-provisioning helpers for embedded audio engines.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package provision

import (
	"encoding/hex"
	"fmt"
	"strings"
)

// archivePins holds the expected SHA-256 (lowercase hex) of every archive
// the embedded STT/TTS providers download, keyed by the archive file name
// (the last path segment of its URL). The engine archive is executed, and
// the model archives feed it, so nothing is extracted unless its digest
// matches the entry here. A missing or empty entry fails closed.
//
// The model archives are published under rolling upstream tags
// (asr-models, tts-models) whose assets can be replaced in place; the digest
// is what pins them to one immutable byte stream. A re-uploaded asset fails
// verification instead of being installed.
//
// Entries marked PIN-PENDING are empty placeholders: those archives fail
// closed until the digest of the named upstream asset is filled in.
//
// Bump SherpaVersion, the engine names below and every digest together.
var archivePins = map[string]string{
	// Engine: https://github.com/k2-fsa/sherpa-onnx/releases/download/v1.13.2/<name>
	// PIN-PENDING: fill with the sha256 of .../v1.13.2/sherpa-onnx-v1.13.2-linux-x64-shared.tar.bz2
	"sherpa-onnx-v" + SherpaVersion + "-linux-x64-shared.tar.bz2": "",
	// PIN-PENDING: fill with the sha256 of .../v1.13.2/sherpa-onnx-v1.13.2-linux-aarch64-shared-cpu.tar.bz2
	"sherpa-onnx-v" + SherpaVersion + "-linux-aarch64-shared-cpu.tar.bz2": "",
	// PIN-PENDING: fill with the sha256 of .../v1.13.2/sherpa-onnx-v1.13.2-osx-x64-shared.tar.bz2
	"sherpa-onnx-v" + SherpaVersion + "-osx-x64-shared.tar.bz2": "",
	// PIN-PENDING: fill with the sha256 of .../v1.13.2/sherpa-onnx-v1.13.2-osx-arm64-shared.tar.bz2
	"sherpa-onnx-v" + SherpaVersion + "-osx-arm64-shared.tar.bz2": "",
	// PIN-PENDING: fill with the sha256 of .../v1.13.2/sherpa-onnx-v1.13.2-win-x64-shared-MT-Release.tar.bz2
	"sherpa-onnx-v" + SherpaVersion + "-win-x64-shared-MT-Release.tar.bz2": "",

	// Whisper models: https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/<name>
	// PIN-PENDING: fill with the sha256 of .../asr-models/sherpa-onnx-whisper-tiny.tar.bz2
	"sherpa-onnx-whisper-tiny.tar.bz2": "",
	// PIN-PENDING: fill with the sha256 of .../asr-models/sherpa-onnx-whisper-base.tar.bz2
	"sherpa-onnx-whisper-base.tar.bz2": "",
	// PIN-PENDING: fill with the sha256 of .../asr-models/sherpa-onnx-whisper-small.tar.bz2
	"sherpa-onnx-whisper-small.tar.bz2": "",
	// PIN-PENDING: fill with the sha256 of .../asr-models/sherpa-onnx-whisper-medium.tar.bz2
	"sherpa-onnx-whisper-medium.tar.bz2": "",
	// PIN-PENDING: fill with the sha256 of .../asr-models/sherpa-onnx-whisper-large-v3.tar.bz2
	"sherpa-onnx-whisper-large-v3.tar.bz2": "",
	// PIN-PENDING: fill with the sha256 of .../asr-models/sherpa-onnx-whisper-tiny.en.tar.bz2
	"sherpa-onnx-whisper-tiny.en.tar.bz2": "",
	// PIN-PENDING: fill with the sha256 of .../asr-models/sherpa-onnx-whisper-base.en.tar.bz2
	"sherpa-onnx-whisper-base.en.tar.bz2": "",
	// PIN-PENDING: fill with the sha256 of .../asr-models/sherpa-onnx-whisper-small.en.tar.bz2
	"sherpa-onnx-whisper-small.en.tar.bz2": "",
	// PIN-PENDING: fill with the sha256 of .../asr-models/sherpa-onnx-whisper-medium.en.tar.bz2
	"sherpa-onnx-whisper-medium.en.tar.bz2": "",

	// Kokoro voice model: https://github.com/k2-fsa/sherpa-onnx/releases/download/tts-models/<name>
	// PIN-PENDING: fill with the sha256 of .../tts-models/kokoro-int8-multi-lang-v1_0.tar.bz2
	"kokoro-int8-multi-lang-v1_0.tar.bz2": "",
}

// PinnedSHA256 returns the expected SHA-256 of the archive called name. It
// fails closed: an archive without a well-formed pin cannot be provisioned,
// and the error says how the user can proceed without it.
func PinnedSHA256(name string) (string, error) {
	return lookupPin(archivePins, name)
}

// PinSource resolves the expected SHA-256 of an archive by file name. The
// providers hold one as an interface (not a func field) so their structs
// stay comparable.
type PinSource interface {
	PinnedSHA256(name string) (string, error)
}

// CompiledPins is the PinSource backed by the table compiled into this build.
type CompiledPins struct{}

// PinnedSHA256 implements PinSource.
func (CompiledPins) PinnedSHA256(name string) (string, error) { return PinnedSHA256(name) }

// FixedPins is a PinSource over an explicit name-to-digest map, with the
// same fail-closed rules as the compiled table.
type FixedPins map[string]string

// PinnedSHA256 implements PinSource.
func (p FixedPins) PinnedSHA256(name string) (string, error) { return lookupPin(p, name) }

func lookupPin(pins map[string]string, name string) (string, error) {
	want := pins[name]
	if raw, err := hex.DecodeString(want); err != nil || len(raw) != 32 {
		return "", fmt.Errorf("no pinned SHA-256 for %s in this build, so it cannot be verified and will not be downloaded; "+
			"use a cloud or command-based provider instead, or pre-seed the cache directory with a verified copy", name)
	}
	return strings.ToLower(want), nil
}
