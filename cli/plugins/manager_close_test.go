/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"testing"
	"time"
)

// closeWithin fails the test when Close does not return: Close waits for
// every reload it knows about, so a reload slot that is never released
// shows up as a hang.
func closeWithin(t *testing.T, m *Manager) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		m.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not return")
	}
}

func setDebounce(m *Manager, d time.Duration) {
	m.bgMu.Lock()
	m.debounce = d
	m.bgMu.Unlock()
}

// A reload that the debounce timer has scheduled but not yet started must
// die with the manager. With an hour of debounce the reload can only be
// pending, so Close returning proves it was cancelled rather than awaited,
// and the stopped timer proves it can never run.
func TestManagerCloseCancelsAPendingReload(t *testing.T) {
	m, _ := newIsolatedManager(t, true)
	setDebounce(m, time.Hour)
	m.scheduleReload()
	m.scheduleReload() // replacing a pending reload must not leak its slot

	closeWithin(t, m)

	m.bgMu.Lock()
	defer m.bgMu.Unlock()
	if m.reloadTimer == nil {
		t.Fatal("no reload was scheduled")
	}
	if m.reloadTimer.Stop() {
		t.Fatal("the pending reload was still armed after Close")
	}
}

// A reload that is already running when Close is called must be finished
// by the time Close returns: nothing of the manager may run afterwards.
func TestManagerCloseWaitsForARunningReload(t *testing.T) {
	m, _ := newIsolatedManager(t, true)
	setDebounce(m, time.Nanosecond)

	// Hold reloadMu so the debounced reload starts and then blocks inside
	// Reload, which is the state Close has to wait out.
	m.reloadMu.Lock()
	m.scheduleReload()
	closed := make(chan struct{})
	go func() {
		m.Close()
		close(closed)
	}()
	select {
	case <-closed:
		m.reloadMu.Unlock()
		t.Fatal("Close returned while a reload was still running")
	case <-time.After(200 * time.Millisecond):
	}
	m.reloadMu.Unlock()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not return after the reload finished")
	}
}

func TestManagerDoesNotScheduleReloadsAfterClose(t *testing.T) {
	m, _ := newIsolatedManager(t, true)
	closeWithin(t, m)

	m.scheduleReload()

	m.bgMu.Lock()
	defer m.bgMu.Unlock()
	if m.reloadTimer != nil {
		t.Fatal("a reload was scheduled on a closed manager")
	}
}
