//go:build windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"errors"
	"math"
	"strconv"
	"syscall"
)

const (
	parkProcessQueryLimited = 0x1000 // PROCESS_QUERY_LIMITED_INFORMATION
	parkStillActive         = 259    // STILL_ACTIVE
)

// parkProcessAlive reports whether pid is a running process. Access denied
// means the process exists under another account, which is alive; only a
// pid that cannot be opened at all, or has an exit code, is gone.
func parkProcessAlive(pid int) bool {
	if pid <= 0 || pid > math.MaxInt32 {
		return false
	}
	h, err := syscall.OpenProcess(parkProcessQueryLimited, false, uint32(pid))
	if err != nil {
		return errors.Is(err, syscall.ERROR_ACCESS_DENIED)
	}
	defer func() { _ = syscall.CloseHandle(h) }()
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return true // it opened: it exists, and that is all that is asked
	}
	return code == parkStillActive
}

// parkProcessStart returns when pid started (its creation time).
func parkProcessStart(pid int) (string, bool) {
	if pid <= 0 || pid > math.MaxInt32 {
		return "", false
	}
	h, err := syscall.OpenProcess(parkProcessQueryLimited, false, uint32(pid))
	if err != nil {
		return "", false
	}
	defer func() { _ = syscall.CloseHandle(h) }()
	var created, exited, kernel, user syscall.Filetime
	if err := syscall.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return "", false
	}
	return "filetime:" + strconv.FormatInt(created.Nanoseconds(), 10), true
}
