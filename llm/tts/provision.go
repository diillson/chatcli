/*
 * ChatCLI - Self-provisioning helpers for the embedded TTS provider.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The embedded provider ships nothing in the chatcli binary (the release is a
 * CGO-free cross-compile): on first use it downloads a prebuilt
 * sherpa-onnx-offline-tts CLI and the Kokoro multi-lang model into the user
 * cache and shells out, the same pattern the transcription package uses for
 * whisper.cpp models. Downloads are atomic — fetch to a .part file, extract
 * into a .tmp directory, rename into place — and a version-stamped marker
 * file is written last, so a crash mid-provision never yields a half cache
 * that masquerades as a working install.
 */
package tts

import (
	"archive/tar"
	"compress/bzip2"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

const (
	// sherpaVersion pins the upstream release providing the prebuilt CLI. The
	// kokoro voice catalog in voices.go tracks the model asset below; bump
	// them together.
	sherpaVersion     = "1.13.2"
	sherpaReleaseBase = "https://github.com/k2-fsa/sherpa-onnx/releases/download/v" + sherpaVersion + "/"
	kokoroModelURL    = "https://github.com/k2-fsa/sherpa-onnx/releases/download/tts-models/kokoro-int8-multi-lang-v1_0.tar.bz2"

	// sherpaBinName is the TTS executable inside the release tarball.
	sherpaBinName = "sherpa-onnx-offline-tts"

	// Size floors reject HTML error pages served with status 200. The shared
	// CLI tarball is ~25MB and the int8 model ~126MB.
	minSherpaBytes = 5 << 20
	minKokoroBytes = 50 << 20

	// maxExtractBytes bounds total decompressed output as a tar-bomb guard.
	maxExtractBytes = 4 << 30

	// provisionTimeout covers the model download on slow links.
	provisionTimeout = 30 * time.Minute
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

// sherpaAsset maps GOOS/GOARCH to the upstream shared-build tarball name.
// Shared builds are ~25MB versus ~200MB+ for static ones; the libraries ride
// in the same tarball so the result is still self-contained.
func sherpaAsset(goos, goarch string) (string, bool) {
	switch goos + "/" + goarch {
	case "linux/amd64":
		return "sherpa-onnx-v" + sherpaVersion + "-linux-x64-shared.tar.bz2", true
	case "linux/arm64":
		return "sherpa-onnx-v" + sherpaVersion + "-linux-aarch64-shared-cpu.tar.bz2", true
	case "darwin/amd64":
		return "sherpa-onnx-v" + sherpaVersion + "-osx-x64-shared.tar.bz2", true
	case "darwin/arm64":
		return "sherpa-onnx-v" + sherpaVersion + "-osx-arm64-shared.tar.bz2", true
	case "windows/amd64":
		return "sherpa-onnx-v" + sherpaVersion + "-win-x64-shared-MT-Release.tar.bz2", true
	}
	return "", false
}

// embeddedCacheDir returns ~/.cache/chatcli/tts (or the OS cache equivalent),
// sibling of the whisper model cache. CHATCLI_TTS_CACHE_DIR relocates it —
// useful for shared caches and air-gapped pre-seeding.
func embeddedCacheDir() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("CHATCLI_TTS_CACHE_DIR")); dir != "" {
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
// searching for well-known names instead of assuming a directory shape.
func locateProvisioned(root string) (provisionedPaths, bool) {
	var p provisionedPaths
	binName := sherpaBinName
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entry: keep scanning the rest
		}
		switch {
		case !d.IsDir() && d.Name() == binName && p.bin == "":
			p.bin = path
		case !d.IsDir() && d.Name() == "voices.bin" && p.voices == "":
			p.voices = path
		case d.IsDir() && d.Name() == "espeak-ng-data" && p.dataDir == "":
			p.dataDir = path
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
	p.libDir = findLibDir(p.bin)
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

// findLibDir resolves the shared-library directory for the binary: the
// upstream tarball puts libraries in lib/ next to bin/; fall back to the
// binary's own directory.
func findLibDir(bin string) string {
	lib := filepath.Join(filepath.Dir(bin), "..", "lib")
	if fi, err := os.Stat(lib); err == nil && fi.IsDir() {
		return filepath.Clean(lib)
	}
	return filepath.Dir(bin)
}

// downloadArchive fetches url to dest atomically, rejecting bodies smaller
// than minBytes (error pages).
func downloadArchive(ctx context.Context, url, dest string, minBytes int64) error {
	client := utils.NewHTTPClient(zap.NewNop(), provisionTimeout)
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
		return fmt.Errorf("download status %d for %s", resp.StatusCode, url)
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
	if n < minBytes {
		_ = os.Remove(tmp)
		return fmt.Errorf("downloaded archive too small (%d bytes) — likely an error response", n)
	}
	return os.Rename(tmp, dest)
}

// extractTarBz2 decompresses and unpacks a .tar.bz2 file into destDir.
func extractTarBz2(archive, destDir string) error {
	f, err := os.Open(archive) // #nosec G304 -- archive is inside our own cache dir
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return extractTar(bzip2.NewReader(f), destDir)
}

// extractTar unpacks a tar stream into destDir, rejecting entries that would
// escape it (path traversal) and bounding total output (tar bomb). Split from
// extractTarBz2 so tests can feed a plain tar built in memory — the stdlib
// bzip2 package only decompresses.
func extractTar(r io.Reader, destDir string) error {
	destDir = filepath.Clean(destDir)
	tr := tar.NewReader(r)
	var written int64
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := safeJoin(destDir, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o750); err != nil {
				return err
			}
		case tar.TypeReg:
			n, err := writeFileFromTar(target, tr, hdr, maxExtractBytes-written)
			if err != nil {
				return err
			}
			written += n
		case tar.TypeSymlink:
			if err := writeSymlinkFromTar(destDir, target, hdr.Linkname); err != nil {
				return err
			}
		default:
			// Hard links, devices and friends do not appear in upstream
			// tarballs; skip rather than fail on exotic entries.
		}
	}
}

// safeJoin joins name under destDir, failing on absolute names or any path
// that escapes destDir.
func safeJoin(destDir, name string) (string, error) {
	target := filepath.Join(destDir, filepath.FromSlash(name)) // Join also cleans
	if !withinDir(destDir, target) {
		return "", fmt.Errorf("archive entry escapes extraction dir: %s", name)
	}
	return target, nil
}

// withinDir reports whether path equals dir or lives beneath it. Both inputs
// must already be clean.
func withinDir(dir, path string) bool {
	return path == dir || strings.HasPrefix(path, dir+string(os.PathSeparator))
}

// writeFileFromTar writes one regular file from the tar stream, preserving
// the executable bit and enforcing the remaining extraction budget.
func writeFileFromTar(target string, tr io.Reader, hdr *tar.Header, budget int64) (int64, error) {
	if budget <= 0 {
		return 0, fmt.Errorf("archive exceeds extraction budget")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return 0, err
	}
	// Preserve the executable bit but drop setuid/setgid and world-write.
	mode := fs.FileMode(hdr.Mode).Perm() & 0o755
	if mode == 0 {
		mode = 0o644
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode) // #nosec G304 -- under our cache dir, safeJoin-validated
	if err != nil {
		return 0, err
	}
	n, copyErr := io.CopyN(out, tr, budget)
	closeErr := out.Close()
	if copyErr != nil && !errors.Is(copyErr, io.EOF) {
		return n, copyErr
	}
	if n == budget {
		// Budget exhausted mid-file — treat as a bomb rather than truncate.
		return n, fmt.Errorf("archive exceeds extraction budget")
	}
	return n, closeErr
}

// writeSymlinkFromTar materializes a symlink, requiring the resolved target
// to stay inside destDir (the upstream tarballs use relative library links).
func writeSymlinkFromTar(destDir, target, linkname string) error {
	if filepath.IsAbs(linkname) {
		return fmt.Errorf("archive symlink with absolute target: %s", linkname)
	}
	resolved := filepath.Join(filepath.Dir(target), filepath.FromSlash(linkname))
	if !withinDir(destDir, resolved) {
		return fmt.Errorf("archive symlink escapes extraction dir: %s", linkname)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return err
	}
	_ = os.Remove(target) // re-extract over a previous attempt
	return os.Symlink(linkname, target)
}

// provisionArchive downloads url and extracts it into targetDir atomically:
// the tarball lands next to targetDir as a temp file, extraction happens in
// targetDir.tmp, and only a fully extracted tree is renamed into place.
func provisionArchive(ctx context.Context, url, targetDir string, minBytes int64) error {
	tmpTar := targetDir + ".tar.bz2.part-src"
	tmpDir := targetDir + ".tmp"
	defer func() {
		_ = os.Remove(tmpTar)
		_ = os.RemoveAll(tmpDir)
	}()

	if err := downloadArchive(ctx, url, tmpTar, minBytes); err != nil {
		return err
	}
	if err := os.RemoveAll(tmpDir); err != nil {
		return err
	}
	if err := extractTarBz2(tmpTar, tmpDir); err != nil {
		return err
	}
	if err := os.RemoveAll(targetDir); err != nil {
		return err
	}
	return os.Rename(tmpDir, targetDir)
}

func fileExistsTTS(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
