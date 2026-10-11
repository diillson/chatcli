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
// The engine digests are the ones GitHub publishes for those release
// assets; the model digests were computed from the downloaded archives
// (2026-10-11), and the kokoro digest was checked both ways.
//
// Bump SherpaVersion, the engine names below and every digest together.
var archivePins = map[string]string{
	// Engine: https://github.com/k2-fsa/sherpa-onnx/releases/download/v1.13.2/<name>
	"sherpa-onnx-v" + SherpaVersion + "-linux-x64-shared.tar.bz2":          "1ef6741535f7af4d69e394fd440a807108036d26ed4f542660191019da5c0daa",
	"sherpa-onnx-v" + SherpaVersion + "-linux-aarch64-shared-cpu.tar.bz2":  "b54178420e9e6ff6c7f308b5f1cde827215b38393356ee0bd2b7595c648b330b",
	"sherpa-onnx-v" + SherpaVersion + "-osx-x64-shared.tar.bz2":            "5accc61eca4a69fc8860f2078c55f61ca96f67b4c311badffa0d6924ca6e1911",
	"sherpa-onnx-v" + SherpaVersion + "-osx-arm64-shared.tar.bz2":          "50c5c04d93113602432a13454d6bf8e5d2624206b985fbd0dd4698454ae6c509",
	"sherpa-onnx-v" + SherpaVersion + "-win-x64-shared-MT-Release.tar.bz2": "55f31de3977e8d80178cbcf1a70c673ce46c64e7ee5725a3ff30e6df2e26e126",

	// Whisper models: https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/<name>
	"sherpa-onnx-whisper-tiny.tar.bz2":      "c46116994e539aa165266d96b325252728429c12535eb9d8b6a2b10f129e66b1",
	"sherpa-onnx-whisper-base.tar.bz2":      "911b2083efd7c0dca2ac3b358b75222660dc09fb716d64fbfc417ba6c99ff3de",
	"sherpa-onnx-whisper-small.tar.bz2":     "486a46afbb7ba798507190ffe02fea2dd726049af212e774537efac6afb210a6",
	"sherpa-onnx-whisper-medium.tar.bz2":    "614b1172557049069d846c29d9399640bce83a4dd6c580decebd9ce2a4f32c33",
	"sherpa-onnx-whisper-large-v3.tar.bz2":  "2d0e134b3b5fc4a0533baf24a0c9d473b629aa47f030af0a165a05f461df7a03",
	"sherpa-onnx-whisper-tiny.en.tar.bz2":   "2bd6cf965c8bb3e068ef9fa2191387ee63a9dfa2a4e37582a8109641c20005dd",
	"sherpa-onnx-whisper-base.en.tar.bz2":   "475bc7052ce299c007f6d5d5407ba8601f819a2867f6eecee510ed17df581542",
	"sherpa-onnx-whisper-small.en.tar.bz2":  "0cdba2b8aaab69e04847f3427cc9709574112e67913a1a84b7fec3a8729faa9a",
	"sherpa-onnx-whisper-medium.en.tar.bz2": "73d95c169a410b5f23a79f8901374b26e0a16a09ea7f02b5e1db983f4cdfdd67",

	// Kokoro voice model: https://github.com/k2-fsa/sherpa-onnx/releases/download/tts-models/<name>
	"kokoro-int8-multi-lang-v1_0.tar.bz2": "4c3052abaa60943a341f193888cf6abd68787dae6ab8ae5c925a706caa247e4e",
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
