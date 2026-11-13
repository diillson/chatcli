//go:build darwin

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"strconv"

	"golang.org/x/sys/unix"
)

// parkProcessStart returns when pid started, from the kernel's process
// table (one sysctl, any user's process).
func parkProcessStart(pid int) (string, bool) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || info == nil || int(info.Proc.P_pid) != pid {
		return "", false
	}
	tv := info.Proc.P_starttime
	return "unix:" + strconv.FormatInt(tv.Sec, 10) + "." + strconv.FormatInt(int64(tv.Usec), 10), true
}
