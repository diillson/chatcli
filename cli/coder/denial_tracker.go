/*
 * ChatCLI - Denial Tracker
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Tracks consecutive and total denials to prevent infinite permission prompting.
 * Inspired by openclaude's denial tracking with configurable thresholds.
 *
 * Behavior:
 *   - After N consecutive denials for the same tool: auto-deny for the rest of the session
 *   - After M total denials across all tools: switch to "ask everything" mode (slower but safer)
 *   - Reset on explicit user action (/policy reset-denials)
 *   - Reset consecutive count on any successful approval
 */
package coder

import (
	"fmt"
	"os"
	"sync"
)

// DenialLevel indicates the current denial tracking state.
type DenialLevel int

const (
	// DenialNormal means denial counts are within normal limits.
	DenialNormal DenialLevel = iota

	// DenialToolBlocked means a specific tool hit its consecutive denial threshold.
	DenialToolBlocked

	// DenialSessionEscalated means total denials exceeded the session threshold.
	// All tools should require explicit approval (no auto-allow).
	DenialSessionEscalated
)

// DenialTrackerConfig controls denial tracking thresholds.
type DenialTrackerConfig struct {
	// MaxConsecutiveDenials per tool before auto-deny for the session.
	MaxConsecutiveDenials int

	// MaxTotalDenials across all tools before escalating the session.
	MaxTotalDenials int
}

// DefaultDenialTrackerConfig returns the default configuration.
// Override via environment variables.
func DefaultDenialTrackerConfig() DenialTrackerConfig {
	cfg := DenialTrackerConfig{
		MaxConsecutiveDenials: 3,
		MaxTotalDenials:       20,
	}
	if v := os.Getenv("CHATCLI_MAX_CONSECUTIVE_DENIALS"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &cfg.MaxConsecutiveDenials); err != nil {
			// ignore invalid env value, keep default
			_ = err
		}
	}
	if v := os.Getenv("CHATCLI_MAX_TOTAL_DENIALS"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &cfg.MaxTotalDenials); err != nil {
			_ = err
		}
	}
	return cfg
}

// toolDenialState tracks denials for a single tool.
type toolDenialState struct {
	consecutiveDenials int
	totalDenials       int
	blocked            bool // auto-blocked for the session
}

// DenialTracker tracks permission denials to prevent infinite prompting.
type DenialTracker struct {
	mu     sync.RWMutex
	config DenialTrackerConfig

	// Per-tool state: key is the normalized tool+subcommand (e.g., "@coder exec")
	tools map[string]*toolDenialState

	// Session-wide counters
	totalDenials int
	escalated    bool // true when total denials exceed threshold
}

// NewDenialTracker creates a new denial tracker.
func NewDenialTracker(config DenialTrackerConfig) *DenialTracker {
	return &DenialTracker{
		config: config,
		tools:  make(map[string]*toolDenialState),
	}
}

// RecordDenial records a denial for the given tool.
// Returns the current denial level after recording.
func (dt *DenialTracker) RecordDenial(toolKey string) DenialLevel {
	dt.mu.Lock()
	defer dt.mu.Unlock()

	state := dt.getOrCreate(toolKey)
	state.consecutiveDenials++
	state.totalDenials++
	dt.totalDenials++

	// Check consecutive threshold
	if state.consecutiveDenials >= dt.config.MaxConsecutiveDenials {
		state.blocked = true
	}

	// Check total threshold
	if dt.totalDenials >= dt.config.MaxTotalDenials {
		dt.escalated = true
	}

	if dt.escalated {
		return DenialSessionEscalated
	}
	if state.blocked {
		return DenialToolBlocked
	}
	return DenialNormal
}

// RecordApproval records an approval for the given tool.
// Resets the consecutive denial count for that tool.
func (dt *DenialTracker) RecordApproval(toolKey string) {
	dt.mu.Lock()
	defer dt.mu.Unlock()

	state := dt.getOrCreate(toolKey)
	state.consecutiveDenials = 0
	// Note: total denials are not reset — they track session-wide behavior
}

// IsBlocked returns true if the given tool is auto-blocked due to consecutive denials.
func (dt *DenialTracker) IsBlocked(toolKey string) bool {
	dt.mu.RLock()
	defer dt.mu.RUnlock()

	state, ok := dt.tools[toolKey]
	if !ok {
		return false
	}
	return state.blocked
}

// IsEscalated returns true if the session is in escalated mode (all tools require approval).
func (dt *DenialTracker) IsEscalated() bool {
	dt.mu.RLock()
	defer dt.mu.RUnlock()
	return dt.escalated
}

// GetLevel returns the current denial level for a tool.
func (dt *DenialTracker) GetLevel(toolKey string) DenialLevel {
	dt.mu.RLock()
	defer dt.mu.RUnlock()

	if dt.escalated {
		return DenialSessionEscalated
	}
	if state, ok := dt.tools[toolKey]; ok && state.blocked {
		return DenialToolBlocked
	}
	return DenialNormal
}

// Reset clears all denial tracking state.
func (dt *DenialTracker) Reset() {
	dt.mu.Lock()
	defer dt.mu.Unlock()

	dt.tools = make(map[string]*toolDenialState)
	dt.totalDenials = 0
	dt.escalated = false
}

// Stats returns a snapshot of denial tracking statistics.
func (dt *DenialTracker) Stats() DenialStats {
	dt.mu.RLock()
	defer dt.mu.RUnlock()

	stats := DenialStats{
		TotalDenials: dt.totalDenials,
		Escalated:    dt.escalated,
		BlockedTools: make([]string, 0),
	}

	for key, state := range dt.tools {
		if state.blocked {
			stats.BlockedTools = append(stats.BlockedTools, key)
		}
	}
	return stats
}

// DenialStats is a snapshot of denial tracking state for display.
type DenialStats struct {
	TotalDenials int
	Escalated    bool
	BlockedTools []string
}

func (dt *DenialTracker) getOrCreate(toolKey string) *toolDenialState {
	state, ok := dt.tools[toolKey]
	if !ok {
		state = &toolDenialState{}
		dt.tools[toolKey] = state
	}
	return state
}
