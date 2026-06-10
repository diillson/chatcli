/*
 * ChatCLI - Self-provisioning helper tests (Kokoro cache layout).
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The generic download/extract machinery is tested in llm/internal/provision;
 * here only the Kokoro-specific cache layout checks remain.
 */
package tts

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

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

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
