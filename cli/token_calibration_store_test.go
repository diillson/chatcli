/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/diillson/chatcli/cli/ctxmgr"
	"github.com/diillson/chatcli/models"
)

func TestCalibrationPersistsPerRoot(t *testing.T) {
	rootA, rootB := t.TempDir(), t.TempDir()
	a := calibratorFor(rootA)
	a.Observe("openai", "gpt-5.6-terra", 30_000, 10_000) // ratio 3.0
	a.flushCalibration()
	if _, err := os.Stat(filepath.Join(rootA, calibrationFileName)); err != nil {
		t.Fatalf("calibration file must be written: %v", err)
	}
	// Another tenant root learns nothing from A.
	if r, n := calibratorFor(rootB).CharsPerToken("openai", "gpt-5.6-terra"); n != 0 || r != defaultCharsPerToken {
		t.Fatalf("tenant isolation: ratio=%v samples=%d", r, n)
	}
	// A fresh process (registry entry dropped) reloads A's ratio.
	calibratorsMu.Lock()
	delete(calibratorsByRoot, rootA)
	calibratorsMu.Unlock()
	if r, n := calibratorFor(rootA).CharsPerToken("openai", "gpt-5.6-terra"); n != 1 || r < 2.99 || r > 3.01 {
		t.Fatalf("persisted ratio must reload: ratio=%v samples=%d", r, n)
	}
	// Corrupt file = empty calibrator, never an error.
	rootC := t.TempDir()
	_ = os.WriteFile(filepath.Join(rootC, calibrationFileName), []byte("{nope"), 0o600)
	if _, n := calibratorFor(rootC).CharsPerToken("x", "y"); n != 0 {
		t.Fatal("corrupt file must load as empty")
	}
	if calibratorFor("") != globalTokenCalibrator {
		t.Fatal("empty root is the process-wide calibrator")
	}
}

func TestEstimatorsFollowTheLearnedRatio(t *testing.T) {
	cli := &ChatCLI{stateRoot: t.TempDir(), Provider: "openai", Model: "gpt-5.6-terra"}
	cli.installTokenEstimator()
	t.Cleanup(func() { ctxmgr.SetCharsPerTokenSource(nil) })
	est := cli.getTokenEstimatorForCurrentLLM()
	if est("abcdefgh") != 2 || ctxmgr.EstimateTokens(8) != 2 || cli.estimateTokens64(8) != 2 {
		t.Fatal("default ratio is 4 chars per token")
	}
	cli.calibrator().Observe("openai", "gpt-5.6-terra", 20_000, 10_000) // 2.0
	if est("abcdefgh") != 4 || ctxmgr.EstimateTokens(8) != 4 || ctxmgr.EstimateTokens64(8) != 4 || cli.estimateTokens64(8) != 4 {
		t.Fatalf("estimators must follow the learned ratio: %d %d %d", est("abcdefgh"), ctxmgr.EstimateTokens(8), cli.estimateTokens64(8))
	}
	cfg := cli.compactConfig("openai", "gpt-5.6-terra")
	if cfg.CharsPerTokenPrecise < 1.99 || cfg.CharsPerTokenPrecise > 2.01 {
		t.Fatalf("compaction budget must use the session calibrator: %v", cfg.CharsPerTokenPrecise)
	}
	// A model switch is reflected lazily (no sync point).
	cli.Model = "gpt-5.5"
	if ctxmgr.EstimateTokens(8) != 2 {
		t.Fatal("another model starts from the default again")
	}
	// The chat turn observer routes to the same scoped calibrator.
	cli.observeTokenCalibrationChars("openai", "gpt-5.5", 60_000, &models.UsageInfo{PromptTokens: 10_000, IsReal: true})
	if r, _ := cli.calibrator().CharsPerToken("openai", "gpt-5.5"); r < 5.99 || r > 6.01 {
		t.Fatalf("observed ratio = %v", r)
	}
	ctxmgr.SetCharsPerTokenSource(func() float64 { return 99 })
	if ctxmgr.CharsPerToken() != 4 {
		t.Fatal("an out-of-band source falls back to the default")
	}
}
