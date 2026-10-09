/*
 * ChatCLI - Headless browser reaper tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMain closes the shared headless browser the render tests may have
// launched: a test binary has no watcher, so without this every run of the
// suite left a headless Chrome behind.
func TestMain(m *testing.M) {
	code := m.Run()
	ShutdownRenderBrowser()
	os.Exit(code)
}

// fakeProcs records what the watcher killed and decides who is alive.
type fakeProcs struct {
	mu      sync.Mutex
	dead    map[int]bool
	killed  []int
	dirKill []string
}

func newFakeProcs() *fakeProcs { return &fakeProcs{dead: map[int]bool{}} }

func (f *fakeProcs) alive(pid int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.dead[pid]
}
func (f *fakeProcs) setDead(pid int) { f.mu.Lock(); f.dead[pid] = true; f.mu.Unlock() }
func (f *fakeProcs) kill(pid int) {
	f.mu.Lock()
	f.killed = append(f.killed, pid)
	f.dead[pid] = true
	f.mu.Unlock()
}
func (f *fakeProcs) killByDir(dir string) {
	f.mu.Lock()
	f.dirKill = append(f.dirKill, dir)
	f.mu.Unlock()
}

func rodProfile(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "rod", "user-data", "abc123")
	require.NoError(t, os.MkdirAll(dir, 0o750))
	return dir
}

func newWatch(f *fakeProcs, dir string, pids chan int, gone chan struct{}) reaperWatch {
	return reaperWatch{
		owner: 1, dataDir: dir, pids: pids, ownerGone: gone,
		every: 5 * time.Millisecond, pidWait: time.Minute,
		alive: f.alive, kill: f.kill, killByDir: f.killByDir,
	}
}

// The owner's pipe closes: the browser is killed by pid and by profile,
// and the profile directory is removed.
func TestReaperWatch_OwnerPipeClosed(t *testing.T) {
	f := newFakeProcs()
	dir := rodProfile(t)
	pids, gone := make(chan int, 1), make(chan struct{})
	pids <- 42
	go func() { time.Sleep(20 * time.Millisecond); close(gone) }()

	assert.Equal(t, 0, newWatch(f, dir, pids, gone).run())
	assert.Equal(t, []int{42}, f.killed)
	assert.Equal(t, []string{dir}, f.dirKill)
	assert.NoDirExists(t, dir)
	assert.DirExists(t, filepath.Dir(dir), "nothing above the profile is touched")
}

// The owner dies while the browser is still starting, before it could
// report the pid: the browser is found by its profile directory.
func TestReaperWatch_OwnerGoneBeforeThePID(t *testing.T) {
	f := newFakeProcs()
	dir := rodProfile(t)
	gone := make(chan struct{})
	close(gone)

	assert.Equal(t, 0, newWatch(f, dir, make(chan int), gone).run())
	assert.Empty(t, f.killed, "no pid was known")
	assert.Equal(t, []string{dir}, f.dirKill, "the browser is reaped by its profile instead")
}

// The owner pid disappears even though the pipe is still open (inherited
// elsewhere): the poll catches it.
func TestReaperWatch_OwnerPIDGone(t *testing.T) {
	f := newFakeProcs()
	pids := make(chan int, 1)
	pids <- 42
	go func() { time.Sleep(20 * time.Millisecond); f.setDead(1) }()

	assert.Equal(t, 0, newWatch(f, rodProfile(t), pids, make(chan struct{})).run())
	assert.Equal(t, []int{42}, f.killed)
}

// The browser closes on its own (idle timer, clean shutdown): the watcher
// leaves without killing anything.
func TestReaperWatch_BrowserClosedNormally(t *testing.T) {
	f := newFakeProcs()
	pids := make(chan int, 1)
	pids <- 42
	go func() { time.Sleep(20 * time.Millisecond); f.setDead(42) }()

	assert.Equal(t, 0, newWatch(f, rodProfile(t), pids, make(chan struct{})).run())
	assert.Empty(t, f.killed)
	assert.Empty(t, f.dirKill)
}

// A launch that never reports a pid ends the watcher after its deadline.
func TestReaperWatch_GivesUpWithoutAPID(t *testing.T) {
	f := newFakeProcs()
	w := newWatch(f, rodProfile(t), make(chan int), make(chan struct{}))
	w.pidWait = 20 * time.Millisecond
	assert.Equal(t, 0, w.run())
	assert.Empty(t, f.killed)
	assert.Empty(t, f.dirKill)
}

func TestReadBrowserPID(t *testing.T) {
	r, wr := io.Pipe()
	pids, gone := readBrowserPID(r)
	_, _ = io.WriteString(wr, "not-a-pid\n4242\n")
	assert.Equal(t, 4242, <-pids)
	select {
	case <-gone:
		t.Fatal("the pipe is still open")
	default:
	}
	_ = wr.Close()
	select {
	case <-gone:
	case <-time.After(2 * time.Second):
		t.Fatal("EOF on the owner's pipe must signal that the owner is gone")
	}
}

func TestIsRodProfileDir(t *testing.T) {
	base := t.TempDir()
	assert.True(t, isRodProfileDir(filepath.Join(base, "rod", "user-data", "6c52a7266b54d503")))
	for _, dir := range []string{
		"",
		"rod/user-data/abc",
		filepath.Join(base, "user-data", "abc"),
		filepath.Join(base, "rod", "profiles", "abc"),
		filepath.Join(base, "rod", "user-data"),
		base,
	} {
		assert.False(t, isRodProfileDir(dir), dir)
	}
}

func TestNewRenderProfileDir(t *testing.T) {
	a, b := newRenderProfileDir(), newRenderProfileDir()
	assert.True(t, isRodProfileDir(a), a)
	assert.NotEqual(t, a, b, "every launch gets its own profile")
	assert.True(t, strings.HasPrefix(a, os.TempDir()))
}

func TestRunBrowserReaper_RejectsBadArguments(t *testing.T) {
	assert.Equal(t, 2, RunBrowserReaper(nil))
	assert.Equal(t, 2, RunBrowserReaper([]string{"--owner", "1"}))
	assert.Equal(t, 2, RunBrowserReaper([]string{"--owner", "1", "--data-dir", "/tmp/not-a-rod-profile"}))
	assert.Equal(t, 2, RunBrowserReaper([]string{"--bogus"}))
}

func TestSpawnBrowserReaper_IsANoOpUntilEnabled(t *testing.T) {
	browserReaper.mu.Lock()
	savedExe, savedSub := browserReaper.exe, browserReaper.subcommand
	browserReaper.exe, browserReaper.subcommand = "", ""
	browserReaper.mu.Unlock()
	defer func() {
		browserReaper.mu.Lock()
		browserReaper.exe, browserReaper.subcommand = savedExe, savedSub
		browserReaper.mu.Unlock()
	}()

	// A test binary must never re-run itself as a watcher.
	w := spawnBrowserReaper(newRenderProfileDir())
	assert.Nil(t, w)
	w.announce(42) // nil-safe
	w.stop()

	EnableBrowserReaper("")
	browserReaper.mu.Lock()
	assert.Empty(t, browserReaper.exe, "an empty subcommand does not enable the watcher")
	browserReaper.mu.Unlock()
}

func TestShutdownRenderBrowser_IsIdempotent(t *testing.T) {
	ShutdownRenderBrowser()
	ShutdownRenderBrowser()
	renderShared.mu.Lock()
	defer renderShared.mu.Unlock()
	assert.Nil(t, renderShared.browser)
	assert.Nil(t, renderShared.launcher)
	assert.Nil(t, renderShared.watch)
}
