/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package mcp

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

// A server that exits before the manager registered its close hook used
// to be lost: the hook was a plain field written after the read loop had
// already started, so the close found it nil and the server kept looking
// alive. The reason must be kept and delivered on registration.
func TestStdioCloseBeforeHookIsDelivered(t *testing.T) {
	tr := &stdioTransport{}
	boom := errors.New("exit status 1")
	tr.fireClose(boom)
	tr.fireClose(errors.New("a later reason must not replace the first"))

	var got []error
	tr.setOnClose(func(err error) { got = append(got, err) })
	if len(got) != 1 || !errors.Is(got[0], boom) {
		t.Fatalf("close before registration: got %v, want exactly [%v]", got, boom)
	}
}

func TestStdioCloseAfterHookFiresOnce(t *testing.T) {
	tr := &stdioTransport{}
	var calls atomic.Int32
	tr.setOnClose(func(error) { calls.Add(1) })
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { tr.fireClose(errors.New("gone")) })
	}
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Fatalf("close hook ran %d times, want 1", n)
	}
}

// Registration racing the read loop of a process that exits at once: the
// hook runs exactly once whichever side wins. Run under -race this is the
// report the old field assignment produced.
func TestStdioHooksRegisteredWhileProcessExits(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs /usr/bin/true")
	}
	for range 20 {
		tr, err := newStdioTransport(context.Background(),
			ServerConfig{Name: "gone", Command: "/usr/bin/true"}, zap.NewNop(), nil)
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		closed := make(chan struct{})
		var calls atomic.Int32
		tr.setOnLog(func(string) {})
		tr.setOnClose(func(error) {
			if calls.Add(1) == 1 {
				close(closed)
			}
		})
		select {
		case <-closed:
		case <-time.After(5 * time.Second):
			t.Fatal("the exit of the process never reached the close hook")
		}
		_ = tr.Close(context.Background())
		if n := calls.Load(); n != 1 {
			t.Fatalf("close hook ran %d times, want 1", n)
		}
	}
}
