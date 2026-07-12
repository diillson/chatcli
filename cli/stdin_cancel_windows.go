//go:build windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"runtime"
	"sync"

	"golang.org/x/sys/windows"
)

var procCancelSynchronousIo = kernel32.NewProc("CancelSynchronousIo")

// stdinReadCanceler aborts a pending blocking os.Stdin.Read on Windows.
//
// A console read has no cancellable deadline: once the reader goroutine is
// inside ReadFile, closing a channel cannot unblock it. The documented API
// for this is CancelSynchronousIo, which targets the OS *thread* issuing the
// synchronous I/O. The reader goroutine therefore locks itself to its OS
// thread (bind) and publishes a duplicated thread handle; stopStdinReader
// calls cancel to make the in-flight ReadFile return ERROR_OPERATION_ABORTED,
// letting the goroutine observe stdinDone and exit cleanly — no synthetic
// input, no console-buffer mutation.
type stdinReadCanceler struct {
	mu     sync.Mutex
	thread windows.Handle
}

func newStdinReadCanceler() *stdinReadCanceler { return &stdinReadCanceler{} }

// bind must be called from the reader goroutine before its first read. It
// pins the goroutine to its OS thread (so every subsequent Read syscall is
// issued from a known thread) and publishes that thread's handle.
func (c *stdinReadCanceler) bind() {
	runtime.LockOSThread()
	cp := windows.CurrentProcess()
	var h windows.Handle
	if err := windows.DuplicateHandle(cp, windows.CurrentThread(), cp, &h, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		return // cancel() degrades to a no-op attempt; the retry loop still waits
	}
	c.mu.Lock()
	c.thread = h
	c.mu.Unlock()
}

// unbind releases the thread handle and the OS-thread pin. Must be called
// from the reader goroutine on exit.
func (c *stdinReadCanceler) unbind() {
	c.mu.Lock()
	h := c.thread
	c.thread = 0
	c.mu.Unlock()
	if h != 0 {
		_ = windows.CloseHandle(h)
	}
	runtime.UnlockOSThread()
}

// cancel aborts the reader thread's in-flight synchronous I/O, if any.
// Returns true because the platform supports cancellation (the caller uses
// this to pick between the cancel-retry path and the plain-wait fallback).
// ERROR_NOT_FOUND — no I/O in flight at this instant — is expected: the
// caller retries, which closes the race where the read starts right after a
// cancel that found nothing.
func (c *stdinReadCanceler) cancel() bool {
	c.mu.Lock()
	h := c.thread
	c.mu.Unlock()
	if h != 0 {
		_, _, _ = procCancelSynchronousIo.Call(uintptr(h))
	}
	return true
}
