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

func TestDownloadArchive(t *testing.T) {
	payload := bytes.Repeat([]byte("A"), 64)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "asset.tar.bz2")
	if err := DownloadArchive(context.Background(), srv.URL, dest, 10); err != nil {
		t.Fatalf("DownloadArchive: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("downloaded bytes mismatch: err %v", err)
	}

	// A body under the floor must be rejected and leave no file behind.
	dest2 := filepath.Join(t.TempDir(), "small.tar.bz2")
	if err := DownloadArchive(context.Background(), srv.URL, dest2, 1024); err == nil {
		t.Fatal("undersized body must be rejected")
	}
	if _, err := os.Stat(dest2); err == nil {
		t.Fatal("rejected download must not leave the dest file")
	}

	// Non-200 must fail.
	srv404 := httptest.NewServer(http.NotFoundHandler())
	defer srv404.Close()
	if err := DownloadArchive(context.Background(), srv404.URL, dest2, 1); err == nil {
		t.Fatal("404 must fail")
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
	if _, err := writeFileFromTar(filepath.Join(dest, "big.bin"), tr, hdr, 5); err == nil {
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
