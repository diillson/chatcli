//go:build linux

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package coder

import (
	"os"

	"golang.org/x/sys/unix"
)

// flushTTYInput discards data the kernel has queued for the controlling
// terminal's input. Linux uses the TCFLSH ioctl with TCIFLUSH to select
// the input queue. We target /dev/tty explicitly so the operation does the
// right thing even if stdin is redirected.
func flushTTYInput() error {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil
	}
	defer func() { _ = tty.Close() }()
	if err := unix.IoctlSetInt(int(tty.Fd()), unix.TCFLSH, unix.TCIFLUSH); err != nil {
		return err
	}
	return nil
}
