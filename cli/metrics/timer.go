/*
 * ChatCLI - Metrics Timer
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package metrics

import (
	"context"
	"sync"
	"time"
)

// Timer representa um cronometro de execução com display em tempo real
type Timer struct {
	startTime    time.Time
	stopTime     time.Time
	running      bool
	displayFunc  func(duration time.Duration)
	cancel       context.CancelFunc
	mu           sync.Mutex
	updateTicker *time.Ticker
	onPause      func() // called under mu when Pause() is invoked
}

// NewTimer cria um novo timer
func NewTimer() *Timer {
	return &Timer{}
}

// Start inicia o timer e opcionalmente atualiza o display em tempo real
func (t *Timer) Start(ctx context.Context, displayFunc func(duration time.Duration)) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.startTime = time.Now()
	t.running = true
	t.displayFunc = displayFunc

	if displayFunc != nil {
		tickerCtx, cancel := context.WithCancel(ctx)
		t.cancel = cancel
		t.updateTicker = time.NewTicker(100 * time.Millisecond)

		go func() {
			for {
				select {
				case <-tickerCtx.Done():
					return
				case <-t.updateTicker.C:
					t.mu.Lock()
					if t.running && t.displayFunc != nil {
						duration := time.Since(t.startTime)
						t.displayFunc(duration)
					}
					t.mu.Unlock()
				}
			}
		}()
	}
}

// Stop para o timer e retorna a duração total
func (t *Timer) Stop() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.running {
		return 0
	}

	t.stopTime = time.Now()
	t.running = false

	if t.cancel != nil {
		t.cancel()
	}
	if t.updateTicker != nil {
		t.updateTicker.Stop()
	}

	return t.stopTime.Sub(t.startTime)
}

// Elapsed retorna o tempo decorrido (funciona mesmo com timer rúdando)
func (t *Timer) Elapsed() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.running {
		return time.Since(t.startTime)
	}
	if !t.stopTime.IsZero() {
		return t.stopTime.Sub(t.startTime)
	}
	return 0
}

// IsRunning retorna se o timer está em execução
func (t *Timer) IsRunning() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.running
}

// SetOnPause registers a callback that runs (under mu) when Pause is called.
// Use this to clear multi-line displays before a security prompt takes over.
// The callback runs under the same mutex as displayFunc, so sharing closure
// variables (like a line counter) between them is safe without extra locking.
func (t *Timer) SetOnPause(f func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.onPause = f
}

// Pause temporarily suppresses the display output without stopping the timer.
// The elapsed time continues accumulating. Call Resume to restore display.
// If an onPause callback was registered, it runs under the mutex before
// pausing, allowing multi-line displays to be properly cleared.
func (t *Timer) Pause() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.running {
		t.running = false
		if t.onPause != nil {
			t.onPause()
		}
	}
}

// Resume restores the display output after a Pause.
func (t *Timer) Resume() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.running && t.cancel != nil {
		t.running = true
	}
}
