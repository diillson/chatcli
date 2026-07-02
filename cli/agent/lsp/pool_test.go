package lsp

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

// pipeSpawner returns a Pool spawn func backed by navFakeServer pipes,
// counting how many servers were actually started.
func pipeSpawner(t *testing.T, spawns *atomic.Int32) func(context.Context, ServerSpec, *zap.Logger) (*Client, error) {
	return func(_ context.Context, _ ServerSpec, _ *zap.Logger) (*Client, error) {
		spawns.Add(1)
		c2sR, c2sW := io.Pipe()
		s2cR, s2cW := io.Pipe()
		go navFakeServer(t, c2sR, s2cW, false)
		return New(c2sW, s2cR, zap.NewNop()), nil
	}
}

func writeGoFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestPoolReusesServerAcrossFiles pins the pool's reason to exist: two files
// in the same project acquire the SAME server — one spawn, one cold start.
func TestPoolReusesServerAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fileA := writeGoFile(t, dir, "a.go", "package x\n")
	fileB := writeGoFile(t, dir, "b.go", "package x\n")

	var spawns atomic.Int32
	p := NewPool(zap.NewNop())
	p.spawn = pipeSpawner(t, &spawns)
	defer p.Close()

	sA, err := p.Acquire(context.Background(), fileA)
	if err != nil {
		t.Fatalf("Acquire A: %v", err)
	}
	sB, err := p.Acquire(context.Background(), fileB)
	if err != nil {
		t.Fatalf("Acquire B: %v", err)
	}
	if spawns.Load() != 1 {
		t.Fatalf("spawned %d servers for one project, want 1", spawns.Load())
	}
	if sA.Client != sB.Client {
		t.Fatal("same project must share one client")
	}
	if sA.Root != dir || sB.Root != dir {
		t.Fatalf("root detection failed: %q / %q, want %q", sA.Root, sB.Root, dir)
	}
}

// TestPoolResyncsChangedContent pins document synchronization: re-acquiring
// an unchanged file is a no-op, re-acquiring after an edit sends a versioned
// didChange (observable here through the version bump in the doc state).
func TestPoolResyncsChangedContent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := writeGoFile(t, dir, "a.go", "package x\n")

	var spawns atomic.Int32
	p := NewPool(zap.NewNop())
	p.spawn = pipeSpawner(t, &spawns)
	defer p.Close()

	if _, err := p.Acquire(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Acquire(context.Background(), file); err != nil { // unchanged
		t.Fatal(err)
	}
	uri := "file://" + file
	p.mu.Lock()
	var version int
	for _, e := range p.entries {
		version = e.docs[uri].version
	}
	p.mu.Unlock()
	if version != 1 {
		t.Fatalf("unchanged file bumped version to %d, want 1 (no-op sync)", version)
	}

	if err := os.WriteFile(file, []byte("package x\n\nfunc F() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Acquire(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	for _, e := range p.entries {
		version = e.docs[uri].version
	}
	p.mu.Unlock()
	if version != 2 {
		t.Fatalf("edited file version = %d, want 2 (didChange sent)", version)
	}
}

// TestPoolReapsIdleServers pins the TTL reaper: a server unused past the TTL
// is shut down and a fresh acquire spawns anew.
func TestPoolReapsIdleServers(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := writeGoFile(t, dir, "a.go", "package x\n")

	var spawns atomic.Int32
	p := NewPool(zap.NewNop())
	p.spawn = pipeSpawner(t, &spawns)
	defer p.Close()

	fakeNow := time.Now()
	p.now = func() time.Time { return fakeNow }

	if _, err := p.Acquire(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	fakeNow = fakeNow.Add(poolIdleTTL + time.Minute)
	if _, err := p.Acquire(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	if spawns.Load() != 2 {
		t.Fatalf("spawned %d, want 2 (idle server reaped, fresh spawn)", spawns.Load())
	}
}

// TestPoolRejectsOversizedFiles pins the document-size guard.
func TestPoolRejectsOversizedFiles(t *testing.T) {
	dir := t.TempDir()
	big := make([]byte, maxDocumentBytes+1)
	for i := range big {
		big[i] = 'a'
	}
	file := writeGoFile(t, dir, "big.go", string(big))

	p := NewPool(zap.NewNop())
	defer p.Close()
	if _, err := p.Acquire(context.Background(), file); err == nil {
		t.Fatal("oversized file must be rejected with a clear error")
	}
}

// TestPoolUnsupportedExtension pins the error message steering the model to
// the supported set.
func TestPoolUnsupportedExtension(t *testing.T) {
	dir := t.TempDir()
	file := writeGoFile(t, dir, "notes.txt", "hello")
	p := NewPool(zap.NewNop())
	defer p.Close()
	if _, err := p.Acquire(context.Background(), file); err == nil {
		t.Fatal("unsupported extension must error")
	}
}

// TestFindProjectRootPrefersMarkers pins module-level rooting: a file nested
// two directories below go.mod roots at the module, not its own directory.
func TestFindProjectRootPrefersMarkers(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "internal", "svc")
	if err := os.MkdirAll(nested, 0o750); err != nil {
		t.Fatal(err)
	}
	file := writeGoFile(t, nested, "svc.go", "package svc\n")

	if got := findProjectRoot(file); got != root {
		t.Fatalf("findProjectRoot = %q, want module root %q", got, root)
	}
	// No markers anywhere: falls back to the file's directory.
	lonely := writeGoFile(t, t.TempDir(), "x.go", "package x\n")
	if got := findProjectRoot(lonely); got != filepath.Dir(lonely) {
		t.Fatalf("markerless root = %q, want file dir", got)
	}
}
