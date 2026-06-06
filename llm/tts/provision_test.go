/*
 * ChatCLI - Self-provisioning helper tests.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package tts

import (
	"archive/tar"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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
		got, ok := sherpaAsset(tt.goos, tt.goarch)
		if got != tt.want || ok != tt.ok {
			t.Errorf("sherpaAsset(%s, %s) = %q, %v; want %q, %v",
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

func TestLocateProvisioned(t *testing.T) {
	root := t.TempDir()
	binName := sherpaBinName
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	mustWrite(t, filepath.Join(root, "sherpa", "bin", binName), "bin")
	mustWrite(t, filepath.Join(root, "sherpa", "lib", "libonnx.so"), "lib")
	mustWrite(t, filepath.Join(root, "kokoro", "voices.bin"), "v")
	mustWrite(t, filepath.Join(root, "kokoro", "tokens.txt"), "t")
	mustWrite(t, filepath.Join(root, "kokoro", "model.int8.onnx"), "m")
	if err := os.MkdirAll(filepath.Join(root, "kokoro", "espeak-ng-data"), 0o750); err != nil {
		t.Fatal(err)
	}

	p, ok := locateProvisioned(root)
	if !ok {
		t.Fatal("complete layout not located")
	}
	if filepath.Base(p.model) != "model.int8.onnx" {
		t.Errorf("model = %s, want model.int8.onnx", p.model)
	}
	if filepath.Base(p.libDir) != "lib" {
		t.Errorf("libDir = %s, want .../lib", p.libDir)
	}

	// Incomplete layout (no tokens.txt) must not pass.
	if err := os.Remove(filepath.Join(root, "kokoro", "tokens.txt")); err != nil {
		t.Fatal(err)
	}
	if _, ok := locateProvisioned(root); ok {
		t.Fatal("layout without tokens.txt must not be considered provisioned")
	}
}

func TestIsProvisionedDir_RequiresMarkerAndLayout(t *testing.T) {
	root := t.TempDir()
	if isProvisionedDir(root) {
		t.Fatal("empty dir must not be provisioned")
	}
	mustWrite(t, readyMarker(root), "")
	if isProvisionedDir(root) {
		t.Fatal("marker without artifacts must not be provisioned")
	}
}

func TestDownloadArchive(t *testing.T) {
	payload := bytes.Repeat([]byte("A"), 64)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "asset.tar.bz2")
	if err := downloadArchive(context.Background(), srv.URL, dest, 10); err != nil {
		t.Fatalf("downloadArchive: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("downloaded bytes mismatch: err %v", err)
	}

	// A body under the floor must be rejected and leave no file behind.
	dest2 := filepath.Join(t.TempDir(), "small.tar.bz2")
	if err := downloadArchive(context.Background(), srv.URL, dest2, 1024); err == nil {
		t.Fatal("undersized body must be rejected")
	}
	if _, err := os.Stat(dest2); err == nil {
		t.Fatal("rejected download must not leave the dest file")
	}

	// Non-200 must fail.
	srv404 := httptest.NewServer(http.NotFoundHandler())
	defer srv404.Close()
	if err := downloadArchive(context.Background(), srv404.URL, dest2, 1); err == nil {
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

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
