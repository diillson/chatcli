//go:build !windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package flock

import (
	"os"
	"syscall"
)

func lockFile(f *os.File) error   { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX) }
func unlockFile(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
