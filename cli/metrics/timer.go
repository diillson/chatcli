/*
 * ChatCLI - Metrics Timer
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package metrics

import (
	"context"
	"fmt"
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

// Elapsed retorna o tempo decorrido (funciona mesmo com timer rodando)
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

// FormatDuration formata uma duração para exibição amigável
func FormatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	minutes := int(d.Minutes())
	seconds := int(d.Seconds()) % 60
	return fmt.Sprintf("%d min %d seg", minutes, seconds)
}

// FormatDurationShort formata duração em formato curto (M:SS)
func FormatDurationShort(d time.Duration) string {
	totalSeconds := int(d.Seconds())
	minutes := totalSeconds / 60
	seconds := totalSeconds % 60
	return fmt.Sprintf("%d:%02d", minutes, seconds)
}
