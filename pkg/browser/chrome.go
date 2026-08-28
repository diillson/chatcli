/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * chrome.go — locating and launching a Chromium-family browser for the
 * @browser tool.
 *
 * ChatCLI talks straight to the DevTools protocol over the websocket the
 * browser announces at startup, so the only requirement is a local Chrome,
 * Chromium, Brave or Edge binary — no driver, no downloaded runtime, no new
 * dependency. CHATCLI_BROWSER_BIN overrides discovery; headless is the
 * default and CHATCLI_BROWSER_HEADLESS=false opens a visible window.
 */
package browser

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// BinEnv overrides browser binary discovery with an explicit path.
const BinEnv = "CHATCLI_BROWSER_BIN"

// HeadlessEnv controls window visibility. Default is headless; set to
// 0/false/off/no to watch the agent drive a real window.
const HeadlessEnv = "CHATCLI_BROWSER_HEADLESS"

// launchWait bounds how long we wait for the browser to announce its
// DevTools websocket endpoint on stderr.
const launchWait = 20 * time.Second

// chromeCandidates lists well-known binary locations/names per OS, most
// specific first. PATH lookup applies to bare names.
func chromeCandidates() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"google-chrome", "chromium",
		}
	case "windows":
		return []string{
			os.ExpandEnv(`${ProgramFiles}\Google\Chrome\Application\chrome.exe`),
			os.ExpandEnv(`${ProgramFiles(x86)}\Google\Chrome\Application\chrome.exe`),
			os.ExpandEnv(`${LocalAppData}\Google\Chrome\Application\chrome.exe`),
			os.ExpandEnv(`${ProgramFiles(x86)}\Microsoft\Edge\Application\msedge.exe`),
			"chrome.exe", "msedge.exe",
		}
	default: // linux and friends
		return []string{
			"google-chrome", "google-chrome-stable", "chromium", "chromium-browser",
			"brave-browser", "microsoft-edge", "/usr/bin/google-chrome",
		}
	}
}

// locateChrome resolves the browser binary: explicit env override first,
// then the per-OS candidate list.
func locateChrome() (string, error) {
	if bin := strings.TrimSpace(os.Getenv(BinEnv)); bin != "" {
		if p, err := exec.LookPath(bin); err == nil {
			return p, nil
		}
		return "", fmt.Errorf("browser binary from %s not found or not executable: %s", BinEnv, bin)
	}
	for _, cand := range chromeCandidates() {
		if cand == "" {
			continue
		}
		if p, err := exec.LookPath(cand); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf(
		"no Chrome/Chromium-family browser found — install Google Chrome, Chromium, Brave or Edge, or point %s at the binary", BinEnv)
}

// headlessEnabled reports whether the browser should run without a window.
func headlessEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(HeadlessEnv))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// launchChrome starts the browser with a throwaway profile and returns the
// running command plus the DevTools websocket URL it announced. The caller
// owns both the process and the userDataDir.
func launchChrome(ctx context.Context) (cmd *exec.Cmd, wsURL, userDataDir string, err error) {
	bin, err := locateChrome()
	if err != nil {
		return nil, "", "", err
	}
	userDataDir, err = os.MkdirTemp("", "chatcli-browser-*")
	if err != nil {
		return nil, "", "", fmt.Errorf("create browser profile dir: %w", err)
	}

	args := []string{
		"--remote-debugging-port=0",
		"--user-data-dir=" + userDataDir,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-background-networking",
		"--disable-sync",
		"--disable-features=Translate",
		"--mute-audio",
		"about:blank",
	}
	if headlessEnabled() {
		args = append([]string{"--headless=new"}, args...)
	}

	cmd = exec.Command(bin, args...) // #nosec G204 -- binary from curated candidates or operator-set CHATCLI_BROWSER_BIN
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = os.RemoveAll(userDataDir)
		return nil, "", "", err
	}
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(userDataDir)
		return nil, "", "", fmt.Errorf("start browser: %w", err)
	}

	wsURL, err = waitForDevTools(ctx, stderr)
	if err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		_ = os.RemoveAll(userDataDir)
		return nil, "", "", err
	}
	// Keep draining stderr so the browser never blocks on a full pipe.
	go func() { _, _ = io.Copy(io.Discard, stderr) }()
	return cmd, wsURL, userDataDir, nil
}

// waitForDevTools scans browser stderr for the "DevTools listening on ws://…"
// announcement, bounded by launchWait and the caller's context.
func waitForDevTools(ctx context.Context, stderr io.Reader) (string, error) {
	const marker = "DevTools listening on "
	found := make(chan string, 1)
	scanErr := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 0, 64*1024), 256*1024)
		for sc.Scan() {
			line := sc.Text()
			if idx := strings.Index(line, marker); idx >= 0 {
				found <- strings.TrimSpace(line[idx+len(marker):])
				return
			}
		}
		scanErr <- fmt.Errorf("browser exited before announcing its DevTools endpoint")
	}()

	timer := time.NewTimer(launchWait)
	defer timer.Stop()
	select {
	case ws := <-found:
		return ws, nil
	case err := <-scanErr:
		return "", err
	case <-timer.C:
		return "", fmt.Errorf("browser did not announce a DevTools endpoint within %s", launchWait)
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
