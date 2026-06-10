/*
 * ChatCLI - Shared self-provisioning helpers for embedded audio engines.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The embedded TTS (Kokoro) and embedded STT (Whisper) providers ship nothing
 * in the chatcli binary (the release is a CGO-free cross-compile): on first
 * use they download a prebuilt sherpa-onnx CLI plus a model archive into the
 * user cache and shell out. This package holds the provider-agnostic pieces:
 * the pinned sherpa-onnx release, atomic download + tar.bz2 extraction with
 * traversal/bomb guards, and the loader-path environment for the extracted
 * shared libraries. Downloads are atomic — fetch to a .part file, extract
 * into a .tmp directory, rename into place — so a crash mid-provision never
 * yields a half cache that masquerades as a working install.
 */
package provision

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
	// SherpaVersion pins the upstream release providing the prebuilt CLIs.
	// Model assets referenced by the embedded providers track this version;
	// bump them together.
	SherpaVersion = "1.13.2"
	// SherpaReleaseBase is the download URL prefix for the pinned release.
	SherpaReleaseBase = "https://github.com/k2-fsa/sherpa-onnx/releases/download/v" + SherpaVersion + "/"

	// maxExtractBytes bounds total decompressed output as a tar-bomb guard.
	maxExtractBytes = 4 << 30

	// Timeout covers a model download on slow links.
	Timeout = 30 * time.Minute
)

// SherpaAsset maps GOOS/GOARCH to the upstream shared-build tarball name.
// Shared builds are ~25MB versus ~200MB+ for static ones; the libraries ride
// in the same tarball so the result is still self-contained.
func SherpaAsset(goos, goarch string) (string, bool) {
	switch goos + "/" + goarch {
	case "linux/amd64":
		return "sherpa-onnx-v" + SherpaVersion + "-linux-x64-shared.tar.bz2", true
	case "linux/arm64":
		return "sherpa-onnx-v" + SherpaVersion + "-linux-aarch64-shared-cpu.tar.bz2", true
	case "darwin/amd64":
		return "sherpa-onnx-v" + SherpaVersion + "-osx-x64-shared.tar.bz2", true
	case "darwin/arm64":
		return "sherpa-onnx-v" + SherpaVersion + "-osx-arm64-shared.tar.bz2", true
	case "windows/amd64":
		return "sherpa-onnx-v" + SherpaVersion + "-win-x64-shared-MT-Release.tar.bz2", true
	}
	return "", false
}

// DownloadArchive fetches url to dest atomically, rejecting bodies smaller
// than minBytes (error pages served with status 200).
func DownloadArchive(ctx context.Context, url, dest string, minBytes int64) error {
	client := utils.NewHTTPClient(zap.NewNop(), Timeout)
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

// ExtractTarBz2 decompresses and unpacks a .tar.bz2 file into destDir.
func ExtractTarBz2(archive, destDir string) error {
	f, err := os.Open(archive) // #nosec G304 -- archive is inside our own cache dir
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return extractTar(bzip2.NewReader(f), destDir)
}

// extractTar unpacks a tar stream into destDir, rejecting entries that would
// escape it (path traversal) and bounding total output (tar bomb). Split from
// ExtractTarBz2 so tests can feed a plain tar built in memory — the stdlib
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
	// Archive entries become either a plain file or an executable — nothing
	// from the tar header beyond the exec bit survives (no setuid/setgid, no
	// world-write), and no integer conversion of untrusted header bits.
	mode := fs.FileMode(0o644)
	if hdr.Mode&0o111 != 0 {
		mode = 0o755
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

// ProvisionArchive downloads url and extracts it into targetDir atomically:
// the tarball lands next to targetDir as a temp file, extraction happens in
// targetDir.tmp, and only a fully extracted tree is renamed into place.
func ProvisionArchive(ctx context.Context, url, targetDir string, minBytes int64) error {
	tmpTar := targetDir + ".tar.bz2.part-src"
	tmpDir := targetDir + ".tmp"
	defer func() {
		_ = os.Remove(tmpTar)
		_ = os.RemoveAll(tmpDir)
	}()

	if err := DownloadArchive(ctx, url, tmpTar, minBytes); err != nil {
		return err
	}
	if err := os.RemoveAll(tmpDir); err != nil {
		return err
	}
	if err := ExtractTarBz2(tmpTar, tmpDir); err != nil {
		return err
	}
	if err := os.RemoveAll(targetDir); err != nil {
		return err
	}
	return os.Rename(tmpDir, targetDir)
}

// FindLibDir resolves the shared-library directory for an extracted engine
// binary: the upstream tarball puts libraries in lib/ next to bin/; fall back
// to the binary's own directory.
func FindLibDir(bin string) string {
	lib := filepath.Join(filepath.Dir(bin), "..", "lib")
	if fi, err := os.Stat(lib); err == nil && fi.IsDir() {
		return filepath.Clean(lib)
	}
	return filepath.Dir(bin)
}

// LibPathEnv returns the process environment with the engine's shared-library
// directory prepended to the platform's loader path. The upstream binaries
// carry an rpath, so this is a belt-and-suspenders for stripped environments.
func LibPathEnv(libDir string) []string {
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
