//go:build !windows

/*
 * ChatCLI - Headless browser reaper (Unix)
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// detachReaper starts the watcher in its own session, so the terminal's
// Ctrl+C or a hang-up that ends chatcli does not end the watcher with it.
func detachReaper(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// processAlive reports whether pid names a running process. EPERM means it
// exists but belongs to someone else, which still counts as alive.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// killBrowserTree kills the browser and its renderer and GPU helpers: go-rod
// starts Chrome as the leader of its own process group.
func killBrowserTree(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

// killBrowsersUsingDir kills every process started with the given profile
// directory (go-rod passes it as --user-data-dir=<dir>): the browser the
// owner never got to report, and its helpers.
func killBrowsersUsingDir(dir string) {
	if !isRodProfileDir(dir) {
		return
	}
	needle := "--user-data-dir=" + dir
	self := os.Getpid()
	for pid, cmdline := range processCommandLines() {
		if pid != self && strings.Contains(cmdline, needle) {
			killBrowserTree(pid)
		}
	}
}

// processCommandLines lists running processes with their command lines:
// from /proc where it exists (Linux), from ps otherwise (macOS, BSDs).
func processCommandLines() map[int]string {
	out := map[int]string{}
	if entries, err := os.ReadDir("/proc"); err == nil && len(entries) > 0 {
		for _, e := range entries {
			pid, err := strconv.Atoi(e.Name())
			if err != nil {
				continue
			}
			raw, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
			if err != nil || len(raw) == 0 {
				continue
			}
			out[pid] = strings.ReplaceAll(string(raw), "\x00", " ")
		}
		if len(out) > 0 {
			return out
		}
	}
	raw, err := exec.Command("ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		sp := strings.IndexByte(line, ' ')
		if sp <= 0 {
			continue
		}
		if pid, err := strconv.Atoi(line[:sp]); err == nil {
			out[pid] = line[sp+1:]
		}
	}
	return out
}
