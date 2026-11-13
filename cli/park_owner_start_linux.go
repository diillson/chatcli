//go:build linux

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"os"
	"strconv"
	"strings"
)

// parkProcessStart returns when pid started, as the kernel records it:
// field 22 of /proc/<pid>/stat, in clock ticks since boot. It is readable
// for any user's process and only ever compared with a value read the
// same way.
func parkProcessStart(pid int) (string, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", false
	}
	// The second field is the command in parentheses and may itself hold
	// spaces and parentheses: count fields from the last ")".
	stat := string(data)
	end := strings.LastIndexByte(stat, ')')
	if end < 0 {
		return "", false
	}
	fields := strings.Fields(stat[end+1:])
	if len(fields) < 20 {
		return "", false
	}
	return "ticks:" + fields[19], true
}
