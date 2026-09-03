/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Learned chars-per-token ratio per provider/model.
 *
 * ChatCLI has no tokenizer: every budget (compaction, projected context
 * use, per-section estimates) was a fixed 4 chars per token, which is off
 * by 2-3x for CJK text, base64 payloads or minified code. The provider,
 * however, reports the real prompt token count on every response, and the
 * request's character count is known — so the ratio can be learned per
 * provider+model and fed back to every estimate. The calibrator is the one
 * place that learning lives; the compactor and /context status read it.
 */
package cli

import (
	"context"
	"github.com/diillson/chatcli/llm/client"
	"go.uber.org/zap"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/models"
)

const (
	// defaultCharsPerToken is the historical fallback before any sample.
	defaultCharsPerToken = 4.0
	// calibrationAlpha is the EMA weight of a new sample — quick to adopt a
	// session's real ratio, slow enough to shrug off one odd request.
	calibrationAlpha = 0.3
	// Ratios outside this band mean the sample paired the wrong request
	// with the wrong usage (or a provider counted something else); they are
	// ignored rather than learned.
	calibrationMinRatio = 1.5
	calibrationMaxRatio = 12.0
	// calibrationMinTokens is the smallest prompt worth learning from — tiny
	// requests are dominated by fixed overhead.
	calibrationMinTokens = 200
)

// tokenCalibrator keeps one EMA ratio per provider:model.
type tokenCalibrator struct {
	mu      sync.RWMutex
	ratios  map[string]float64
	samples map[string]int
	// path is the persistence file ("" = memory only, the process-wide
	// default); saveTimer debounces writes.
	path      string
	saveTimer *time.Timer
}

// globalTokenCalibrator is process-wide: the REPL, the gateway daemon and
// the MCP server each run one process, and every surface's compactor
// should benefit from what any turn learned.
var globalTokenCalibrator = newTokenCalibrator()

func newTokenCalibrator() *tokenCalibrator {
	return &tokenCalibrator{ratios: map[string]float64{}, samples: map[string]int{}}
}

func calibrationKey(provider, model string) string {
	return strings.ToLower(strings.TrimSpace(provider)) + ":" + strings.ToLower(strings.TrimSpace(model))
}

// Observe folds one (request chars, real prompt tokens) pair into the ratio
// for provider/model. Out-of-band samples are ignored.
func (c *tokenCalibrator) Observe(provider, model string, chars, tokens int) {
	if c == nil || chars <= 0 || tokens < calibrationMinTokens {
		return
	}
	ratio := float64(chars) / float64(tokens)
	if ratio < calibrationMinRatio || ratio > calibrationMaxRatio {
		return
	}
	key := calibrationKey(provider, model)
	c.mu.Lock()
	defer c.mu.Unlock()
	if prev, ok := c.ratios[key]; ok {
		c.ratios[key] = prev*(1-calibrationAlpha) + ratio*calibrationAlpha
	} else {
		c.ratios[key] = ratio
	}
	c.samples[key]++
	c.scheduleSave()
}

// CharsPerToken returns the learned ratio and how many samples produced it;
// (defaultCharsPerToken, 0) before any sample.
func (c *tokenCalibrator) CharsPerToken(provider, model string) (float64, int) {
	if c == nil {
		return defaultCharsPerToken, 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	key := calibrationKey(provider, model)
	if r, ok := c.ratios[key]; ok && r > 0 {
		return r, c.samples[key]
	}
	return defaultCharsPerToken, 0
}

// EstimateTokens converts a character count to tokens with the learned
// ratio (or the default), never returning a negative value.
func (c *tokenCalibrator) EstimateTokens(provider, model string, chars int) int {
	if chars <= 0 {
		return 0
	}
	ratio, _ := c.CharsPerToken(provider, model)
	return int(float64(chars)/ratio + 0.5)
}

// promptCharsOf is the character weight of what a request carries: the
// same per-message weight the compactor budgets against, so learned ratios
// and budgets measure the same thing.
func promptCharsOf(history []models.Message) int {
	total := 0
	for _, m := range history {
		total += messageWeight(m)
	}
	return total
}

// observeTokenCalibrationChars feeds one chat turn into the calibrator with
// the request weight measured by the caller — the chat pipeline passes what the wire
// actually carried (temp history plus the turn input), which cli.history
// does not reflect at either end of a chat turn.
func (cli *ChatCLI) observeTokenCalibrationChars(provider, model string, chars int, usage *models.UsageInfo) {
	if usage == nil || !usage.IsReal || chars <= 0 {
		return
	}
	cli.calibrator().Observe(provider, model, chars, contextTokens(provider, model, usage))
}

// calibrateExactEvery paces exact calibration: one count_tokens call per
// this many chat turns keeps the ratio anchored without a round trip on
// every turn.
const calibrateExactEvery = 8

// calibrateExact counts the live history with the provider's own counter
// (client.TokenCounter) and folds the exact pair into the calibrator.
// Returns the counted tokens; ok is false when the client cannot count or
// the call failed — callers fall back to the learned ratio.
func (cli *ChatCLI) calibrateExact(ctx context.Context) (int, bool) {
	if cli == nil || cli.Client == nil || len(cli.history) == 0 {
		return 0, false
	}
	tc, ok := client.AsTokenCounter(cli.Client)
	if !ok {
		return 0, false
	}
	tokens, err := tc.CountTokens(ctx, "", cli.history)
	if err != nil || tokens <= 0 {
		if err != nil && cli.logger != nil {
			cli.logger.Debug("exact token count unavailable; keeping the learned ratio", zap.Error(err))
		}
		return 0, false
	}
	cli.calibrator().Observe(cli.Provider, cli.Model, promptCharsOf(cli.history), tokens)
	return tokens, true
}

// maybeCalibrateExact runs calibrateExact every calibrateExactEvery chat
// turns (and on the first), bounded so it never stalls a turn.
func (cli *ChatCLI) maybeCalibrateExact(ctx context.Context) {
	if cli == nil {
		return
	}
	cli.calibrationTurns++
	if (cli.calibrationTurns-1)%calibrateExactEvery != 0 {
		return
	}
	if _, ok := client.AsTokenCounter(cli.Client); !ok {
		return
	}
	opCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cli.calibrateExact(opCtx)
}
