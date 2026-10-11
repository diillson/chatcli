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
	"crypto/sha256"
	"encoding/hex"
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

// SherpaAssetBytes is the approximate size of the engine tarball SherpaAsset
// names, for surfaces that tell the user how much a first use downloads.
// The figures track SherpaVersion; 0 means no prebuilt engine.
func SherpaAssetBytes(goos, goarch string) int64 {
	switch goos + "/" + goarch {
	case "linux/amd64":
		return 26_825_365
	case "linux/arm64":
		return 26_674_802
	case "darwin/amd64":
		return 28_956_076
	case "darwin/arm64":
		return 25_914_829
	case "windows/amd64":
		return 23_368_301
	}
	return 0
}

// DownloadArchive fetches url to dest atomically, rejecting bodies smaller
// than minBytes (error pages served with status 200) and any body whose
// SHA-256 differs from the pin for the archive name (see PinnedSHA256).
func DownloadArchive(ctx context.Context, url, dest string, minBytes int64) error {
	want, err := PinnedSHA256(archiveName(url))
	if err != nil {
		return err
	}
	return downloadVerified(ctx, url, dest, want, minBytes)
}

// downloadVerified streams url into dest+".part" while hashing it and only
// renames it into place when the digest equals wantSHA256 (hex).
func downloadVerified(ctx context.Context, url, dest, wantSHA256 string, minBytes int64) error {
	if raw, err := hex.DecodeString(wantSHA256); err != nil || len(raw) != sha256.Size {
		return fmt.Errorf("refusing to download %s without a valid expected SHA-256", archiveName(url))
	}
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
	hasher := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(out, hasher), progressBody(ctx, resp.Body, url, resp.ContentLength))
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
	if got := hex.EncodeToString(hasher.Sum(nil)); !strings.EqualFold(got, wantSHA256) {
		_ = os.Remove(tmp)
		return fmt.Errorf("checksum mismatch for %s: expected sha256 %s, got %s — the download was discarded",
			archiveName(url), strings.ToLower(wantSHA256), got)
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

// extractTar unpacks a tar stream into destDir. Every filesystem operation
// goes through an os.Root opened at destDir, so no entry — including one
// reached through a chain of symlinks the archive itself created — can
// create, truncate or link anything outside it: the confinement makes an
// escape impossible by construction rather than by path arithmetic. On top
// of that, entries with absolute or ".." names are refused, symlink and
// hard-link targets must stay inside destDir, every symlink is re-resolved
// inside the root once the archive is fully unpacked (an archive cannot
// leave behind a link that points out), total output is bounded (tar bomb)
// and only the exec bit survives from header modes. Split from
// ExtractTarBz2 so tests can feed a plain tar built in memory — the stdlib
// bzip2 package only decompresses.
func extractTar(r io.Reader, destDir string) error {
	destDir = filepath.Clean(destDir)
	if err := os.MkdirAll(destDir, 0o750); err != nil {
		return err
	}
	root, err := os.OpenRoot(destDir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()

	tr := tar.NewReader(r)
	var written int64
	var links []string
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name, err := entryName(hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(name, 0o750); err != nil {
				return err
			}
		case tar.TypeReg:
			n, err := writeFileFromTar(root, name, tr, hdr, maxExtractBytes-written)
			if err != nil {
				return err
			}
			written += n
		case tar.TypeSymlink:
			if err := writeSymlinkFromTar(root, name, hdr.Linkname); err != nil {
				return err
			}
			links = append(links, name)
		case tar.TypeLink:
			if err := writeHardlinkFromTar(root, name, hdr.Linkname); err != nil {
				return err
			}
		default:
			// Devices, FIFOs and friends do not appear in upstream
			// tarballs; skip rather than fail on exotic entries.
		}
	}
	return verifyLinksStayInside(root, links)
}

// entryName validates an archive member name and returns it as a local,
// OS-separated path relative to the extraction root.
func entryName(raw string) (string, error) {
	name := filepath.Clean(filepath.FromSlash(raw))
	if !filepath.IsLocal(name) {
		return "", fmt.Errorf("archive entry escapes extraction dir: %s", raw)
	}
	return name, nil
}

// writeFileFromTar writes one regular file from the tar stream, preserving
// the executable bit and enforcing the remaining extraction budget.
func writeFileFromTar(root *os.Root, name string, tr io.Reader, hdr *tar.Header, budget int64) (int64, error) {
	if budget <= 0 {
		return 0, fmt.Errorf("archive exceeds extraction budget")
	}
	if err := root.MkdirAll(filepath.Dir(name), 0o750); err != nil {
		return 0, err
	}
	// Archive entries become either a plain file or an executable — nothing
	// from the tar header beyond the exec bit survives (no setuid/setgid, no
	// world-write), and no integer conversion of untrusted header bits.
	mode := fs.FileMode(0o644)
	if hdr.Mode&0o111 != 0 {
		mode = 0o755
	}
	// A previous entry may sit at this name (a symlink or an earlier copy);
	// remove it so the new entry never writes through it.
	if err := removeIfPresent(root, name); err != nil {
		return 0, err
	}
	out, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
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

// localLinkTarget checks a link target lexically: relative, and inside the
// root once joined with the directory holding the link. It returns the
// target in OS form.
func localLinkTarget(name, linkname string) (string, error) {
	target := filepath.FromSlash(linkname)
	if linkname == "" || filepath.IsAbs(target) || filepath.VolumeName(target) != "" || strings.HasPrefix(linkname, "/") {
		return "", fmt.Errorf("archive link with absolute target: %s", linkname)
	}
	if !filepath.IsLocal(filepath.Join(filepath.Dir(name), target)) {
		return "", fmt.Errorf("archive link escapes extraction dir: %s", linkname)
	}
	return target, nil
}

// writeSymlinkFromTar materializes a symlink inside the root. The lexical
// check rejects the obvious escapes early. A chain of links that only
// escapes once resolved (s -> ., s/s2 -> ., s/s2/t -> ../..) passes every
// lexical check, so it is handled structurally: the link is created through
// the root, which refuses to resolve its parent outside destDir, later
// entries written through it are confined the same way, and every link is
// re-resolved after extraction by verifyLinksStayInside.
func writeSymlinkFromTar(root *os.Root, name, linkname string) error {
	target, err := localLinkTarget(name, linkname)
	if err != nil {
		return err
	}
	if err := root.MkdirAll(filepath.Dir(name), 0o750); err != nil {
		return err
	}
	if err := removeIfPresent(root, name); err != nil { // re-extract over a previous attempt
		return err
	}
	return root.Symlink(target, name)
}

// writeHardlinkFromTar materializes a hard link to a regular file already
// extracted inside the root. Both ends are resolved by the root, so neither
// can reach outside destDir.
func writeHardlinkFromTar(root *os.Root, name, linkname string) error {
	oldname, err := entryName(linkname)
	if err != nil {
		return fmt.Errorf("archive hard link escapes extraction dir: %s", linkname)
	}
	fi, err := root.Lstat(oldname)
	if err != nil {
		return fmt.Errorf("archive hard link to missing entry %s: %w", linkname, err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("archive hard link to non-regular entry: %s", linkname)
	}
	if err := root.MkdirAll(filepath.Dir(name), 0o750); err != nil {
		return err
	}
	if err := removeIfPresent(root, name); err != nil {
		return err
	}
	return root.Link(oldname, name)
}

// removeIfPresent deletes name inside the root when it exists, without
// following a final symlink.
func removeIfPresent(root *os.Root, name string) error {
	if _, err := root.Lstat(name); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	return root.Remove(name)
}

// verifyLinksStayInside resolves every extracted symlink through the root
// once the whole archive is in place. os.Root refuses any resolution that
// leaves destDir, so a link whose target escapes — directly or through
// other links — fails here and the extraction is rejected. A dangling link
// is rejected too: nothing the engine needs is a link to nothing.
func verifyLinksStayInside(root *os.Root, links []string) error {
	for _, name := range links {
		if _, err := root.Stat(name); err != nil {
			return fmt.Errorf("archive symlink %s does not resolve inside the extraction dir: %w", filepath.ToSlash(name), err)
		}
	}
	return nil
}

// ProvisionArchive downloads url and extracts it into targetDir atomically:
// the tarball lands next to targetDir as a temp file, extraction happens in
// targetDir.tmp, and only a fully extracted tree is renamed into place. The
// download must match the pinned SHA-256 for the archive name.
func ProvisionArchive(ctx context.Context, url, targetDir string, minBytes int64) error {
	want, err := PinnedSHA256(archiveName(url))
	if err != nil {
		return err
	}
	return ProvisionVerifiedArchive(ctx, url, want, targetDir, minBytes)
}

// ProvisionVerifiedArchive is ProvisionArchive with an explicit expected
// SHA-256 (hex). Nothing is extracted unless the download matches it.
func ProvisionVerifiedArchive(ctx context.Context, url, wantSHA256, targetDir string, minBytes int64) error {
	tmpTar := targetDir + ".tar.bz2.part-src"
	tmpDir := targetDir + ".tmp"
	defer func() {
		_ = os.Remove(tmpTar)
		_ = os.RemoveAll(tmpDir)
	}()

	if err := downloadVerified(ctx, url, tmpTar, wantSHA256, minBytes); err != nil {
		return err
	}
	if err := os.RemoveAll(tmpDir); err != nil {
		return err
	}
	report(ctx, Progress{Phase: PhaseExtract, File: archiveName(url), Done: -1, Total: -1})
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
