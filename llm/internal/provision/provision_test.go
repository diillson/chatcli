/*
 * ChatCLI - Shared self-provisioning helper tests.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package provision

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSherpaAsset(t *testing.T) {
	tests := []struct {
		goos, goarch string
		want         string
		ok           bool
	}{
		{"linux", "amd64", "sherpa-onnx-v1.13.2-linux-x64-shared.tar.bz2", true},
		{"linux", "arm64", "sherpa-onnx-v1.13.2-linux-aarch64-shared-cpu.tar.bz2", true},
		{"darwin", "amd64", "sherpa-onnx-v1.13.2-osx-x64-shared.tar.bz2", true},
		{"darwin", "arm64", "sherpa-onnx-v1.13.2-osx-arm64-shared.tar.bz2", true},
		{"windows", "amd64", "sherpa-onnx-v1.13.2-win-x64-shared-MT-Release.tar.bz2", true},
		{"freebsd", "amd64", "", false},
		{"linux", "riscv64", "", false},
	}
	for _, tt := range tests {
		got, ok := SherpaAsset(tt.goos, tt.goarch)
		if got != tt.want || ok != tt.ok {
			t.Errorf("SherpaAsset(%s, %s) = %q, %v; want %q, %v",
				tt.goos, tt.goarch, got, ok, tt.want, tt.ok)
		}
	}
}

// tarEntry describes one file for buildTar.
type tarEntry struct {
	name string
	body string
	mode int64
	dir  bool
	link string // symlink target when non-empty
	hard string // hard-link target when non-empty
}

func buildTar(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: e.mode}
		if hdr.Mode == 0 {
			hdr.Mode = 0o644
		}
		switch {
		case e.dir:
			hdr.Typeflag = tar.TypeDir
		case e.link != "":
			hdr.Typeflag = tar.TypeSymlink
			hdr.Linkname = e.link
		case e.hard != "":
			hdr.Typeflag = tar.TypeLink
			hdr.Linkname = e.hard
		default:
			hdr.Typeflag = tar.TypeReg
			hdr.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header %s: %v", e.name, err)
		}
		if hdr.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatalf("write body %s: %v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	return buf.Bytes()
}

func TestExtractTar_RegularFilesAndDirs(t *testing.T) {
	dest := t.TempDir()
	data := buildTar(t, []tarEntry{
		{name: "pkg/", dir: true},
		{name: "pkg/bin/tool", body: "binary", mode: 0o755},
		{name: "pkg/readme.txt", body: "hello"},
	})
	if err := extractTar(bytes.NewReader(data), dest); err != nil {
		t.Fatalf("extractTar: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "pkg", "bin", "tool"))
	if err != nil || string(got) != "binary" {
		t.Fatalf("extracted file = %q, err %v", got, err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(dest, "pkg", "bin", "tool"))
		if err != nil || fi.Mode().Perm()&0o100 == 0 {
			t.Fatalf("executable bit lost: mode %v err %v", fi.Mode(), err)
		}
	}
}

func TestExtractTar_RejectsPathTraversal(t *testing.T) {
	dest := t.TempDir()
	data := buildTar(t, []tarEntry{{name: "../evil.txt", body: "x"}})
	if err := extractTar(bytes.NewReader(data), dest); err == nil {
		t.Fatal("path traversal entry must be rejected")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dest), "evil.txt")); err == nil {
		t.Fatal("traversal file was written outside dest")
	}
}

func TestExtractTar_RejectsEscapingSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	dest := t.TempDir()
	data := buildTar(t, []tarEntry{{name: "lib/evil", link: "../../outside"}})
	if err := extractTar(bytes.NewReader(data), dest); err == nil {
		t.Fatal("escaping symlink must be rejected")
	}
}

func TestExtractTar_RelativeSymlinkInside(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	dest := t.TempDir()
	data := buildTar(t, []tarEntry{
		{name: "lib/libreal.dylib", body: "lib"},
		{name: "lib/libalias.dylib", link: "libreal.dylib"},
	})
	if err := extractTar(bytes.NewReader(data), dest); err != nil {
		t.Fatalf("extractTar: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "lib", "libalias.dylib"))
	if err != nil || string(got) != "lib" {
		t.Fatalf("symlink unresolved: %q err %v", got, err)
	}
}

func TestDownloadVerified(t *testing.T) {
	payload := bytes.Repeat([]byte("A"), 64)
	sum := sha256.Sum256(payload)
	want := hex.EncodeToString(sum[:])
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "asset.tar.bz2")
	if err := downloadVerified(context.Background(), srv.URL, dest, want, 10); err != nil {
		t.Fatalf("downloadVerified: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("downloaded bytes mismatch: err %v", err)
	}

	// A body under the floor must be rejected and leave no file behind.
	dest2 := filepath.Join(t.TempDir(), "small.tar.bz2")
	if err := downloadVerified(context.Background(), srv.URL, dest2, want, 1024); err == nil {
		t.Fatal("undersized body must be rejected")
	}
	if _, err := os.Stat(dest2); err == nil {
		t.Fatal("rejected download must not leave the dest file")
	}

	// Non-200 must fail.
	srv404 := httptest.NewServer(http.NotFoundHandler())
	defer srv404.Close()
	if err := downloadVerified(context.Background(), srv404.URL, dest2, want, 1); err == nil {
		t.Fatal("404 must fail")
	}
}

// TestDownloadVerifiedRejectsTamperedBody: a body whose digest differs
// from the pin is discarded, never renamed into place.
func TestDownloadVerifiedRejectsTamperedBody(t *testing.T) {
	sum := sha256.Sum256([]byte("genuine archive"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("tampered archive"))
	}))
	defer srv.Close()
	dir := t.TempDir()
	dest := filepath.Join(dir, "engine.tar.bz2")
	err := downloadVerified(context.Background(), srv.URL+"/engine.tar.bz2", dest, hex.EncodeToString(sum[:]), 1)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("tampered body must be refused, got %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("nothing may be left behind, found %v", entries)
	}
	for _, bad := range []string{"", "PENDING", strings.Repeat("0", 63)} {
		if err := downloadVerified(context.Background(), srv.URL, dest, bad, 1); err == nil {
			t.Fatalf("expected hash %q must be refused", bad)
		}
	}
}

// TestUnpinnedArchiveFailsClosed: the exported entry points look the pin
// up by archive name; a name without a pin is never downloaded.
func TestUnpinnedArchiveFailsClosed(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_, _ = w.Write([]byte("payload"))
	}))
	defer srv.Close()
	dir := t.TempDir()
	if err := DownloadArchive(context.Background(), srv.URL+"/unknown.tar.bz2", filepath.Join(dir, "a"), 1); err == nil {
		t.Fatal("DownloadArchive must refuse an unpinned archive")
	}
	if err := ProvisionArchive(context.Background(), srv.URL+"/unknown.tar.bz2", filepath.Join(dir, "b"), 1); err == nil {
		t.Fatal("ProvisionArchive must refuse an unpinned archive")
	}
	if hits != 0 {
		t.Fatal("an unpinned archive must not even be requested")
	}
}

// TestPinTableCoversEveryDownload: every engine platform, whisper size and
// the kokoro model has an entry, so no download path is outside the table.
// Placeholders are allowed to be empty (fail closed) but never malformed.
func TestPinTableCoversEveryDownload(t *testing.T) {
	names := []string{"kokoro-int8-multi-lang-v1_0.tar.bz2"}
	for _, p := range [][2]string{{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "amd64"}, {"darwin", "arm64"}, {"windows", "amd64"}} {
		asset, ok := SherpaAsset(p[0], p[1])
		if !ok {
			t.Fatalf("no asset for %v", p)
		}
		names = append(names, asset)
	}
	for _, size := range []string{"tiny", "base", "small", "medium", "large-v3", "tiny.en", "base.en", "small.en", "medium.en"} {
		names = append(names, "sherpa-onnx-whisper-"+size+".tar.bz2")
	}
	for _, n := range names {
		want, ok := archivePins[n]
		if !ok {
			t.Errorf("no pin entry for %s", n)
			continue
		}
		if want == "" {
			if _, err := PinnedSHA256(n); err == nil {
				t.Errorf("empty placeholder for %s must fail closed", n)
			}
			continue
		}
		if raw, err := hex.DecodeString(want); err != nil || len(raw) != sha256.Size {
			t.Errorf("pin for %s is not a hex SHA-256: %q", n, want)
		}
	}
	if len(archivePins) != len(names) {
		t.Errorf("pin table has %d entries, downloads need %d", len(archivePins), len(names))
	}
}

func TestProvisionVerifiedArchive(t *testing.T) {
	// A real .tar.bz2 fixture (the stdlib bzip2 package only decompresses):
	// bin/sherpa-onnx-offline + lib/libfake.so.
	payload, err := os.ReadFile(filepath.Join("..", "..", "transcription", "testdata", "fake-sherpa-asr.tar.bz2"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()
	target := filepath.Join(t.TempDir(), "engine")
	if err := ProvisionVerifiedArchive(context.Background(), srv.URL+"/e.tar.bz2", hex.EncodeToString(sum[:]), target, 1); err != nil {
		t.Fatalf("ProvisionVerifiedArchive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "bin", "sherpa-onnx-offline")); err != nil {
		t.Fatalf("engine not extracted: %v", err)
	}

	other := sha256.Sum256([]byte("something else"))
	target2 := filepath.Join(t.TempDir(), "engine")
	if err := ProvisionVerifiedArchive(context.Background(), srv.URL+"/e.tar.bz2", hex.EncodeToString(other[:]), target2, 1); err == nil {
		t.Fatal("digest mismatch must abort provisioning")
	}
	if _, err := os.Stat(target2); err == nil {
		t.Fatal("nothing may be extracted after a digest mismatch")
	}
}

func TestExtractTar_BudgetRejectsBomb(t *testing.T) {
	dest := t.TempDir()
	data := buildTar(t, []tarEntry{{name: "big.bin", body: "0123456789"}})
	// Shrink the budget by feeding a pre-consumed writer: emulate via a tiny
	// budget through writeFileFromTar directly.
	tr := tar.NewReader(bytes.NewReader(data))
	hdr, err := tr.Next()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if _, err := writeFileFromTar(root, "big.bin", tr, hdr, 5); err == nil {
		t.Fatal("exceeding the byte budget must fail")
	}
}

func TestFindLibDir(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin", "engine")
	if err := os.MkdirAll(filepath.Dir(bin), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Without a sibling lib/ the binary's own dir is the lib dir.
	if got := FindLibDir(bin); got != filepath.Dir(bin) {
		t.Fatalf("FindLibDir without lib/ = %s, want %s", got, filepath.Dir(bin))
	}
	if err := os.MkdirAll(filepath.Join(root, "lib"), 0o750); err != nil {
		t.Fatal(err)
	}
	if got := FindLibDir(bin); got != filepath.Join(root, "lib") {
		t.Fatalf("FindLibDir = %s, want %s", got, filepath.Join(root, "lib"))
	}
}

// LibPathEnv is asserted directly: macOS SIP strips DYLD_* before /bin/sh
// (a fake engine script) starts, so a child-process assertion is impossible
// in CI. The real engines are executed directly — not through a shell — and
// also carry an rpath, so the variable is belt-and-suspenders there.
func TestLibPathEnv(t *testing.T) {
	var wantVar string
	switch runtime.GOOS {
	case "linux":
		wantVar = "LD_LIBRARY_PATH"
	case "darwin":
		wantVar = "DYLD_LIBRARY_PATH"
	case "windows":
		wantVar = "PATH"
	default:
		t.Skip("no loader path variable on this platform")
	}
	libDir := filepath.Join("some", "cache", "lib")
	for _, line := range LibPathEnv(libDir) {
		if strings.HasPrefix(line, wantVar+"=") && strings.Contains(line, libDir) {
			return
		}
	}
	t.Fatalf("LibPathEnv missing %s entry containing %s", wantVar, libDir)
}

func TestLibPathEnv_PrependsToExisting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH always exists on windows; covered by TestLibPathEnv")
	}
	varName := "LD_LIBRARY_PATH"
	if runtime.GOOS == "darwin" {
		varName = "DYLD_LIBRARY_PATH"
	}
	t.Setenv(varName, "/pre/existing")
	libDir := filepath.Join("some", "cache", "lib")
	prefix := varName + "=" + libDir + string(os.PathListSeparator) + "/pre/existing"
	for _, line := range LibPathEnv(libDir) {
		if line == prefix {
			return
		}
	}
	t.Fatalf("LibPathEnv did not prepend %s to existing value", libDir)
}
