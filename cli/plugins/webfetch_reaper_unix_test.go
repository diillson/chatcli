//go:build !windows

/*
 * ChatCLI - Headless browser reaper tests with real processes (Unix)
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startFakeBrowser starts a shell as the leader of its own process group,
// the way go-rod starts Chrome, carrying the profile flag on its command
// line, with a sleeping child in that group standing in for a helper.
func startFakeBrowser(t *testing.T, dataDir string) *exec.Cmd {
	t.Helper()
	leader := exec.Command("sh", "-c", "sleep 300 & wait", "fake-chrome", "--user-data-dir="+dataDir)
	leader.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, leader.Start())
	go func() { _ = leader.Wait() }()
	t.Cleanup(func() { killBrowserTree(leader.Process.Pid) })
	return leader
}

func groupGone(pgid int) bool { return syscall.Kill(-pgid, 0) != nil }

// The real watcher logic against real processes: the owner exits before it
// reports the pid, and the browser group is found by its profile and killed.
func TestReaperWatch_RealProcesses_FoundByProfile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rod", "user-data", "e2e0001")
	require.NoError(t, os.MkdirAll(dir, 0o750))
	browser := startFakeBrowser(t, dir)
	pgid := browser.Process.Pid

	owner := exec.Command("sleep", "0.3")
	require.NoError(t, owner.Start())
	go func() { _ = owner.Wait() }()

	w := reaperWatch{
		owner: owner.Process.Pid, dataDir: dir,
		pids: make(chan int), ownerGone: make(chan struct{}),
		every: 50 * time.Millisecond, pidWait: time.Minute,
		alive: processAlive, kill: killBrowserTree, killByDir: killBrowsersUsingDir,
	}
	done := make(chan int, 1)
	go func() { done <- w.run() }()
	select {
	case code := <-done:
		assert.Equal(t, 0, code)
	case <-time.After(10 * time.Second):
		t.Fatal("the watcher did not act after the owner exited")
	}

	assert.Eventually(t, func() bool { return groupGone(pgid) }, 3*time.Second, 50*time.Millisecond,
		"no process of the browser's group survives")
	assert.NoDirExists(t, dir)
}

// killBrowsersUsingDir only touches processes started with that profile.
func TestKillBrowsersUsingDir_OnlyThatProfile(t *testing.T) {
	base := filepath.Join(t.TempDir(), "rod", "user-data")
	mine := startFakeBrowser(t, filepath.Join(base, "mine"))
	other := startFakeBrowser(t, filepath.Join(base, "other"))

	killBrowsersUsingDir(filepath.Join(base, "mine"))

	assert.Eventually(t, func() bool { return groupGone(mine.Process.Pid) }, 3*time.Second, 50*time.Millisecond)
	assert.True(t, processAlive(other.Process.Pid), "a browser with another profile is left alone")
}

func TestProcessAlive(t *testing.T) {
	assert.True(t, processAlive(syscall.Getpid()))
	assert.False(t, processAlive(0))
	assert.False(t, processAlive(-1))

	short := exec.Command("true")
	require.NoError(t, short.Run())
	assert.False(t, processAlive(short.Process.Pid), "a finished, reaped process is not alive")
}

// End to end through the real watcher process: this test binary is run as
// the watcher, learns the browser pid over the pipe, and reaps the browser
// group and its profile as soon as the pipe closes, which is what the
// owner's death looks like to it.
func TestBrowserWatch_RealWatcherReapsOnPipeEOF(t *testing.T) {
	browserReaper.mu.Lock()
	savedExe, savedSub := browserReaper.exe, browserReaper.subcommand
	browserReaper.mu.Unlock()
	t.Cleanup(func() {
		browserReaper.mu.Lock()
		browserReaper.exe, browserReaper.subcommand = savedExe, savedSub
		browserReaper.mu.Unlock()
	})
	EnableBrowserReaper(testReaperSubcommand)

	dir := newRenderProfileDir()
	require.NoError(t, os.MkdirAll(dir, 0o750))
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	watch := spawnBrowserReaper(dir)
	require.NotNil(t, watch, "the watcher starts once enabled")
	browser := startFakeBrowser(t, dir)
	watch.announce(browser.Process.Pid)

	_ = watch.pipe.Close() // the owner is gone

	assert.Eventually(t, func() bool { return groupGone(browser.Process.Pid) }, 10*time.Second, 50*time.Millisecond,
		"the watcher kills the browser group once its owner is gone")
	assert.Eventually(t, func() bool { _, err := os.Stat(dir); return os.IsNotExist(err) }, 5*time.Second, 50*time.Millisecond,
		"and removes the profile")
	assert.Eventually(t, func() bool { return !processAlive(watch.cmd.Process.Pid) }, 5*time.Second, 50*time.Millisecond,
		"and exits")
}

// A watcher whose browser never started is stopped by its owner.
func TestBrowserWatch_StopEndsTheWatcher(t *testing.T) {
	browserReaper.mu.Lock()
	savedExe, savedSub := browserReaper.exe, browserReaper.subcommand
	browserReaper.mu.Unlock()
	t.Cleanup(func() {
		browserReaper.mu.Lock()
		browserReaper.exe, browserReaper.subcommand = savedExe, savedSub
		browserReaper.mu.Unlock()
	})
	EnableBrowserReaper(testReaperSubcommand)

	watch := spawnBrowserReaper(newRenderProfileDir())
	require.NotNil(t, watch)
	watch.stop()
	assert.Eventually(t, func() bool { return !processAlive(watch.cmd.Process.Pid) }, 5*time.Second, 50*time.Millisecond)
}
