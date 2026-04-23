//go:build windows

/*
 * ChatCLI - cmd/daemon_windows.go
 *
 * Platform shim: Windows lacks Setsid, but CREATE_NEW_PROCESS_GROUP
 * gives us the equivalent — the child no longer receives Ctrl+C
 * propagated through the parent's console.
 */
package cmd

import "syscall"

func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: 0x00000200} // CREATE_NEW_PROCESS_GROUP
}
