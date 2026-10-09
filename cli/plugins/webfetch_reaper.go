/*
 * ChatCLI - Headless browser reaper
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The shared headless browser (webfetch_render.go) is launched with go-rod's
 * Leakless helper disabled, because corporate antivirus products quarantine
 * the helper binary it ships. Its idle timer closes the browser while the
 * process lives, but nothing closed it when the process ended first: a
 * chatcli that exited right after a JS-rendered fetch, a one-shot run, a
 * crash, an os.Exit path or a test binary each left a headless Chrome
 * running for good.
 *
 * Two layers close that gap:
 *   - ShutdownRenderBrowser closes the browser on a clean exit;
 *   - every launch is preceded by a small watcher, the chatcli binary itself
 *     run as a hidden subcommand. It is started BEFORE the browser, with the
 *     browser's profile directory and a pipe on its stdin; the browser pid is
 *     written to the pipe once known. When the pipe reaches EOF (the owner
 *     is gone, however it ended) or the owner pid disappears, the watcher
 *     kills the browser by pid and, should the owner have died before
 *     reporting it, by its profile directory, which appears on the browser's
 *     command line. Being our own binary, it is not the helper antivirus
 *     products flag.
 *
 * The watcher only runs when the program opted in with EnableBrowserReaper
 * (the chatcli main does), so a test binary or a library user never re-runs
 * itself as a watcher.
 */

package plugins

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// browserReaperPoll is how often the watcher checks the processes it guards.
const browserReaperPoll = 2 * time.Second

// browserReaperPIDWait bounds how long the watcher waits for the browser pid
// while its owner still runs. A launch that never reports one failed, and
// the owner stops the watcher itself in that case; this is the backstop.
const browserReaperPIDWait = 2 * time.Minute

var browserReaper struct {
	mu         sync.Mutex
	exe        string
	subcommand string
}

// EnableBrowserReaper makes every headless browser launch start a watcher:
// the running executable invoked as `<exe> <subcommand> --owner … --data-dir …`.
// The program must route that subcommand to RunBrowserReaper.
func EnableBrowserReaper(subcommand string) {
	exe, err := os.Executable()
	if err != nil || subcommand == "" {
		return
	}
	browserReaper.mu.Lock()
	browserReaper.exe, browserReaper.subcommand = exe, subcommand
	browserReaper.mu.Unlock()
}

// ShutdownRenderBrowser closes the shared headless browser now, if one is
// running. Safe to call any number of times.
func ShutdownRenderBrowser() {
	renderShared.mu.Lock()
	defer renderShared.mu.Unlock()
	renderShared.closeLocked()
}

// newRenderProfileDir returns a fresh profile directory path in the place
// go-rod would pick by itself (<tmp>/rod/user-data/<random>). Choosing it
// here lets the watcher know it before the browser starts.
func newRenderProfileDir() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return filepath.Join(os.TempDir(), "rod", "user-data", hex.EncodeToString(b[:]))
}

// browserWatch is the owner's handle on a running watcher.
type browserWatch struct {
	cmd  *exec.Cmd
	pipe io.WriteCloser
}

// spawnBrowserReaper starts the watcher for a browser about to be launched
// with the given profile directory. It returns nil when the watcher is not
// enabled or could not start: the idle timer and ShutdownRenderBrowser
// still close the browser while this process runs.
func spawnBrowserReaper(dataDir string) *browserWatch {
	browserReaper.mu.Lock()
	exe, sub := browserReaper.exe, browserReaper.subcommand
	browserReaper.mu.Unlock()
	if exe == "" || dataDir == "" {
		return nil
	}
	cmd := exec.Command(exe, sub, "--owner", strconv.Itoa(os.Getpid()), "--data-dir", dataDir) // #nosec G204 -- runs this very executable (os.Executable) with arguments built here: a pid and a generated profile path
	cmd.Stdout, cmd.Stderr = nil, nil
	pipe, err := cmd.StdinPipe()
	if err != nil {
		return nil
	}
	detachReaper(cmd)
	if err := cmd.Start(); err != nil {
		_ = pipe.Close()
		return nil
	}
	// Reap the watcher if it ends while we still run (the browser closed
	// normally), so it never lingers as a zombie.
	go func() { _ = cmd.Wait() }()
	return &browserWatch{cmd: cmd, pipe: pipe}
}

// announce tells the watcher the pid of the browser it guards. The pipe is
// kept open: its EOF is how the watcher learns that this process is gone.
func (w *browserWatch) announce(pid int) {
	if w == nil || pid <= 0 {
		return
	}
	_, _ = fmt.Fprintf(w.pipe, "%d\n", pid)
}

// stop ends a watcher whose browser never started.
func (w *browserWatch) stop() {
	if w == nil {
		return
	}
	_ = w.pipe.Close()
	if w.cmd.Process != nil {
		_ = w.cmd.Process.Kill()
	}
}

// RunBrowserReaper is the body of the hidden watcher subcommand. It returns
// the process exit code.
func RunBrowserReaper(args []string) int {
	fs := flag.NewFlagSet("browser-reaper", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	owner := fs.Int("owner", 0, "")
	dataDir := fs.String("data-dir", "", "")
	if err := fs.Parse(args); err != nil || *owner <= 0 || !isRodProfileDir(*dataDir) {
		return 2
	}
	pids, ownerGone := readBrowserPID(os.Stdin)
	w := reaperWatch{
		owner:     *owner,
		dataDir:   *dataDir,
		pids:      pids,
		ownerGone: ownerGone,
		every:     browserReaperPoll,
		pidWait:   browserReaperPIDWait,
		alive:     processAlive,
		kill:      killBrowserTree,
		killByDir: killBrowsersUsingDir,
	}
	return w.run()
}

// readBrowserPID turns the owner's pipe into two signals: the browser pid
// when it is announced, and the pipe's end, which means the owner is gone.
func readBrowserPID(r io.Reader) (<-chan int, <-chan struct{}) {
	pids := make(chan int, 1)
	gone := make(chan struct{})
	go func() {
		defer close(gone)
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			if pid, err := strconv.Atoi(strings.TrimSpace(sc.Text())); err == nil && pid > 0 {
				select {
				case pids <- pid:
				default:
				}
			}
		}
	}()
	return pids, gone
}

// reaperWatch is the watcher's state; the process operations are injected
// so the decision logic is testable.
type reaperWatch struct {
	owner     int
	dataDir   string
	pids      <-chan int
	ownerGone <-chan struct{}
	every     time.Duration
	pidWait   time.Duration
	alive     func(int) bool
	kill      func(int)
	killByDir func(string)
}

// run waits until the browser ends on its own (nothing to do) or its owner
// is gone, and then kills the browser and removes its profile directory.
func (w reaperWatch) run() int {
	browser := 0
	tick := time.NewTicker(w.every)
	defer tick.Stop()
	deadline := time.Now().Add(w.pidWait)
	for {
		select {
		case pid := <-w.pids:
			browser = pid
		case <-w.ownerGone:
			w.reap(browser)
			return 0
		case <-tick.C:
			if !w.alive(w.owner) {
				w.reap(browser)
				return 0
			}
			if browser > 0 && !w.alive(browser) {
				return 0 // closed normally (idle timer, clean shutdown)
			}
			if browser == 0 && time.Now().After(deadline) {
				return 0 // the launch never completed
			}
		}
	}
}

// reap kills the browser, by pid when known and by profile directory in
// any case (the owner may have died before reporting the pid), then removes
// the profile.
func (w reaperWatch) reap(browser int) {
	if browser > 0 {
		w.kill(browser)
	}
	w.killByDir(w.dataDir)
	for i := 0; i < 10 && browser > 0 && w.alive(browser); i++ {
		time.Sleep(200 * time.Millisecond)
	}
	if isRodProfileDir(w.dataDir) {
		_ = os.RemoveAll(w.dataDir)
	}
}

// isRodProfileDir accepts only a directory directly under go-rod's own
// "rod/user-data" tree, the only kind the watcher ever deletes or matches.
func isRodProfileDir(dir string) bool {
	if dir == "" || !filepath.IsAbs(dir) {
		return false
	}
	clean := filepath.Clean(dir)
	parent := filepath.Dir(clean)
	return filepath.Base(parent) == "user-data" &&
		filepath.Base(filepath.Dir(parent)) == "rod" &&
		filepath.Base(clean) != "" && filepath.Base(clean) != "." &&
		clean != parent
}
