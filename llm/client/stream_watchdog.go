/*
 * ChatCLI - Stream Watchdog
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Monitors a streaming channel for stalls (no data for too long) and
 * automatically terminates the stream, returning partial content.
 *
 * Inspired by openclaude's 90-second idle watchdog with stall detection.
 */
package client

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// WatchdogConfig controls stream monitoring behavior.
type WatchdogConfig struct {
	// IdleTimeout is the maximum time to wait between chunks before aborting.
	// Default: 90 seconds.
	IdleTimeout time.Duration

	// WarningTimeout is when to log a warning about slow streaming.
	// Default: 45 seconds (half of IdleTimeout).
	WarningTimeout time.Duration
}

// DefaultWatchdogConfig returns the default watchdog configuration.
func DefaultWatchdogConfig() WatchdogConfig {
	idle := 90 * time.Second
	if v := os.Getenv("CHATCLI_STREAM_IDLE_TIMEOUT_SECONDS"); v != "" {
		var secs int
		if _, err := fmt.Sscanf(v, "%d", &secs); err == nil && secs > 0 {
			idle = time.Duration(secs) * time.Second
		}
	}

	return WatchdogConfig{
		IdleTimeout:    idle,
		WarningTimeout: idle / 2,
	}
}

// WatchdogResult contains the outcome of a monitored stream.
type WatchdogResult struct {
	Text       string
	Usage      *models.UsageInfo
	StopReason string
	WasStalled bool // true if the watchdog triggered (partial content)
	StallCount int  // number of stalls detected during streaming
}

// WatchStream monitors a streaming channel and returns accumulated content.
// If the stream stalls for longer than IdleTimeout, it cancels and returns
// whatever content has been accumulated so far.
func WatchStream(ctx context.Context, chunks <-chan StreamChunk, config WatchdogConfig, logger *zap.Logger) *WatchdogResult {
	result := &WatchdogResult{}

	var accumulated string
	warningTimer := time.NewTimer(config.WarningTimeout)
	defer warningTimer.Stop()
	idleTimer := time.NewTimer(config.IdleTimeout)
	defer idleTimer.Stop()

	lastChunkTime := time.Now()

	for {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				// Channel closed
				result.Text = accumulated
				return result
			}

			if chunk.Error != nil {
				result.Text = accumulated
				return result
			}

			accumulated += chunk.Text
			lastChunkTime = time.Now()

			// Reset timers
			if !warningTimer.Stop() {
				select {
				case <-warningTimer.C:
				default:
				}
			}
			warningTimer.Reset(config.WarningTimeout)

			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
			idleTimer.Reset(config.IdleTimeout)

			if chunk.Done {
				result.Text = accumulated
				result.Usage = chunk.Usage
				result.StopReason = chunk.StopReason
				return result
			}

		case <-warningTimer.C:
			stallDuration := time.Since(lastChunkTime)
			result.StallCount++
			if logger != nil {
				logger.Warn("Stream stall detected — still waiting for data",
					zap.Duration("stall_duration", stallDuration),
					zap.Int("stall_count", result.StallCount),
					zap.Int("accumulated_chars", len(accumulated)))
			}

		case <-idleTimer.C:
			stallDuration := time.Since(lastChunkTime)
			if logger != nil {
				logger.Error("Stream idle timeout — returning partial content",
					zap.Duration("stall_duration", stallDuration),
					zap.Int("accumulated_chars", len(accumulated)))
			}
			result.Text = accumulated
			result.WasStalled = true
			result.StallCount++
			return result

		case <-ctx.Done():
			result.Text = accumulated
			return result
		}
	}
}
