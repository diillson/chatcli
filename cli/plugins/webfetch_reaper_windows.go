//go:build windows

/*
 * ChatCLI - Headless browser reaper (Windows)
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// stillActive is the exit code Windows reports for a running process.
const stillActive = 259

// detachReaper starts the watcher without a console and outside chatcli's
// process group, so closing the console does not end it.
func detachReaper(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}
}

// processAlive reports whether pid names a running process.
func processAlive(pid int) bool {
	if pid <= 0 || uint64(pid) > math.MaxUint32 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		// Access denied means the process exists.
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}

// killBrowserTree terminates the browser, as go-rod's own Kill does on
// Windows; its helpers exit with it.
func killBrowserTree(pid int) {
	if pid <= 0 || uint64(pid) > math.MaxUint32 {
		return
	}
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return
	}
	defer func() { _ = windows.CloseHandle(h) }()
	_ = windows.TerminateProcess(h, 1)
}

// killBrowsersUsingDir terminates every process started with the given
// profile directory (go-rod passes it as --user-data-dir=<dir>): the browser
// the owner never got to report, and its helpers.
func killBrowsersUsingDir(dir string) {
	if !isRodProfileDir(dir) {
		return
	}
	needle := strings.ReplaceAll("--user-data-dir="+dir, "'", "''")
	script := fmt.Sprintf(
		"Get-CimInstance Win32_Process | Where-Object { $_.ProcessId -ne %d -and $_.CommandLine -and $_.CommandLine.Contains('%s') } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }",
		os.Getpid(), needle)
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script) // #nosec G204 -- fixed script; the only input is a validated rod profile path with quotes escaped
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	_ = cmd.Run()
}
