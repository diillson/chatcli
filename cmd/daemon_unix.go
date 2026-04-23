//go:build !windows

/*
 * ChatCLI - cmd/daemon_unix.go
 *
 * Platform shim: Setsid on Linux/macOS so the child process escapes
 * the terminal's process group and survives the parent shell closing.
 */
package cmd

import "syscall"

func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
