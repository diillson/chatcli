/*
 * ChatCLI - Self-provisioning helpers for the embedded TTS provider.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The embedded provider ships nothing in the chatcli binary (the release is a
 * CGO-free cross-compile): on first use it downloads a prebuilt
 * sherpa-onnx-offline-tts CLI and the Kokoro multi-lang model into the user
 * cache and shells out. The generic download/extract machinery lives in
 * llm/internal/provision (shared with the embedded STT provider); this file
 * keeps the Kokoro-specific cache layout.
 */
package tts

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/diillson/chatcli/llm/internal/provision"
)

const (
	// sherpaVersion pins the upstream release providing the prebuilt CLI. The
	// kokoro voice catalog in voices.go tracks the model asset below; bump
	// them together.
	sherpaVersion     = provision.SherpaVersion
	sherpaReleaseBase = provision.SherpaReleaseBase
	kokoroModelURL    = "https://github.com/k2-fsa/sherpa-onnx/releases/download/tts-models/kokoro-int8-multi-lang-v1_0.tar.bz2"

	// sherpaBinName is the TTS executable inside the release tarball.
	sherpaBinName = "sherpa-onnx-offline-tts"

	// Size floors reject HTML error pages served with status 200. The shared
	// CLI tarball is ~25MB and the int8 model ~126MB.
	minSherpaBytes = 5 << 20
	minKokoroBytes = 50 << 20

	// provisionTimeout covers the model download on slow links.
	provisionTimeout = provision.Timeout
)

// provisionedPaths locates every artifact Synthesize needs after the cache is
// provisioned.
type provisionedPaths struct {
	bin     string // sherpa-onnx-offline-tts executable
	libDir  string // shared libraries shipped next to the binary
	model   string // model*.onnx
	voices  string // voices.bin
	tokens  string // tokens.txt
	dataDir string // espeak-ng-data
}

// embeddedCacheDir returns ~/.cache/chatcli/tts (or the OS cache equivalent),
// sibling of the whisper model cache. CHATCLI_TTS_CACHE_DIR relocates it —
// useful for shared caches and air-gapped pre-seeding.
func embeddedCacheDir() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("CHATCLI_TTS_CACHE_DIR")); dir != "" {
		// Operator-supplied override: canonicalize and require an absolute
		// path so a relative value can never resolve against an unexpected
		// working directory (the daemon and the REPL have different cwds).
		dir = filepath.Clean(dir)
		if !filepath.IsAbs(dir) {
			return "", fmt.Errorf("tts: CHATCLI_TTS_CACHE_DIR must be an absolute path, got %q", dir)
		}
		return dir, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "chatcli", "tts"), nil
}

// readyMarker is the file whose presence certifies a complete provision for
// the pinned version. It is written only after both archives extracted.
func readyMarker(root string) string {
	return filepath.Join(root, ".ready-v"+sherpaVersion)
}

// isProvisionedDir reports whether root holds a complete, version-matching
// install without touching the network.
func isProvisionedDir(root string) bool {
	if !fileExistsTTS(readyMarker(root)) {
		return false
	}
	_, ok := locateProvisioned(root)
	return ok
}

// locateProvisioned walks the cache root and resolves every artifact path.
// The walk tolerates upstream layout changes (binary in bin/ today) by
// searching for well-known names instead of assuming a directory shape. It
// runs inside an os.Root, so traversal is kernel-confined to the cache
// directory — a symlink or a hostile override can never lead it elsewhere.
func locateProvisioned(root string) (provisionedPaths, bool) {
	confined, err := os.OpenRoot(root)
	if err != nil {
		return provisionedPaths{}, false
	}
	defer func() { _ = confined.Close() }()

	var p provisionedPaths
	binName := sherpaBinName
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	_ = fs.WalkDir(confined.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entry: keep scanning the rest
		}
		abs := filepath.Join(root, filepath.FromSlash(rel))
		switch {
		case !d.IsDir() && d.Name() == binName && p.bin == "":
			p.bin = abs
		case !d.IsDir() && d.Name() == "voices.bin" && p.voices == "":
			p.voices = abs
		case d.IsDir() && d.Name() == "espeak-ng-data" && p.dataDir == "":
			p.dataDir = abs
			return fs.SkipDir // thousands of phoneme files; nothing to find inside
		}
		return nil
	})
	if p.bin == "" || p.voices == "" || p.dataDir == "" {
		return provisionedPaths{}, false
	}
	modelDir := filepath.Dir(p.voices)
	p.tokens = filepath.Join(modelDir, "tokens.txt")
	if !fileExistsTTS(p.tokens) {
		return provisionedPaths{}, false
	}
	if p.model = findONNXModel(modelDir); p.model == "" {
		return provisionedPaths{}, false
	}
	p.libDir = provision.FindLibDir(p.bin)
	return p, true
}

// findONNXModel returns the model file in dir — model.int8.onnx for the int8
// asset, model.onnx for the fp32 one.
func findONNXModel(dir string) string {
	matches, _ := filepath.Glob(filepath.Join(dir, "*.onnx"))
	if len(matches) == 0 {
		return ""
	}
	return matches[0]
}

func fileExistsTTS(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
