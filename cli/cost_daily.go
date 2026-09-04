/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Daily budget: spend accumulated across every session of the calendar
 * day under one store directory. The session budget bounds one run; the
 * daily budget bounds a principal — the store directory is the tenant
 * root under the gateway, so each tenant has its own ceiling. Spend
 * increments are folded in whenever the session total grows and
 * persisted (debounced, atomic) to daily-spend.json; a new day resets.
 */
package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/utils"
)

const (
	dailySpendFile      = "daily-spend.json"
	dailySpendSaveDelay = 1500 * time.Millisecond
)

type dailySpendFileData struct {
	Date     string  `json:"date"`
	SpentUSD float64 `json:"spent_usd"`
	Updated  string  `json:"updated"`
}

func (ct *CostTracker) dailySpendPath() string {
	dir := ct.storeDir
	if dir == "" {
		dir = costStoreDir()
	}
	return filepath.Join(dir, dailySpendFile)
}

func todayKey() string { return time.Now().Format("2006-01-02") }

// loadDailySpendLocked reads today's spend (an older date starts at zero).
func (ct *CostTracker) loadDailySpendLocked() {
	ct.dailyDate = todayKey()
	ct.dailySpentUSD = 0
	ct.dailyBaseline = ct.totalCostUSD
	data, err := os.ReadFile(ct.dailySpendPath()) // #nosec G304 -- fixed file under the cost store dir
	if err != nil {
		return
	}
	var f dailySpendFileData
	if err := json.Unmarshal(data, &f); err != nil || f.Date != ct.dailyDate || f.SpentUSD < 0 {
		return
	}
	ct.dailySpentUSD = f.SpentUSD
}

// accrueDailyLocked folds the growth of the session total since the last
// call into today's spend (a day rollover resets first). Caller holds ct.mu.
func (ct *CostTracker) accrueDailyLocked() {
	if today := todayKey(); today != ct.dailyDate {
		ct.dailyDate = today
		ct.dailySpentUSD = 0
	}
	delta := ct.totalCostUSD - ct.dailyBaseline
	ct.dailyBaseline = ct.totalCostUSD
	if delta <= 0 {
		return
	}
	ct.dailySpentUSD += delta
	if ct.dailySaveTimer != nil {
		ct.dailySaveTimer.Stop()
	}
	// The path is resolved now, not when the timer fires: a late timer
	// must write to the store it was scheduled for, never to whatever HOME
	// points at 1.5s later (another tenant, another test's directory).
	path := ct.dailySpendPath()
	ct.dailySaveTimer = time.AfterFunc(dailySpendSaveDelay, func() { ct.saveDailySpendTo(path) })
}

// saveDailySpend persists today's spend (best-effort, atomic).
func (ct *CostTracker) saveDailySpend() {
	ct.mu.RLock()
	path := ct.dailySpendPath()
	ct.mu.RUnlock()
	ct.saveDailySpendTo(path)
}

// saveDailySpendTo writes today's spend to path.
func (ct *CostTracker) saveDailySpendTo(path string) {
	ct.mu.RLock()
	f := dailySpendFileData{Date: ct.dailyDate, SpentUSD: ct.dailySpentUSD, Updated: time.Now().Format(time.RFC3339)}
	ct.mu.RUnlock()
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	_ = utils.AtomicWriteFile(path, data, 0o600)
}

// FlushDailySpend writes today's spend now (session end, tenant swap).
func (ct *CostTracker) FlushDailySpend() {
	if ct == nil {
		return
	}
	ct.mu.Lock()
	if ct.dailySaveTimer != nil {
		ct.dailySaveTimer.Stop()
		ct.dailySaveTimer = nil
	}
	ct.mu.Unlock()
	ct.saveDailySpend()
}

// printDailyBudgetLine renders the /cost daily-budget line (caller holds
// ct.mu read lock); nothing when no daily budget is configured.
func printDailyBudgetLine(p string, ct *CostTracker) {
	if ct.dailyLimitUSD <= 0 {
		return
	}
	fmt.Println(p)
	line := i18n.T("cost.cmd.daily_budget", fmt.Sprintf("$%.4f", ct.dailySpentUSD), fmt.Sprintf("$%.2f", ct.dailyLimitUSD), ct.dailyDate)
	color := ColorCyan
	switch {
	case ct.dailySpentUSD >= ct.dailyLimitUSD:
		color = ColorRed
	case ct.dailySpentUSD >= ct.dailyLimitUSD*ct.budgetWarningPct:
		color = ColorYellow
	}
	fmt.Println(p + colorize("  "+line, color))
}

// DailyBudget returns today's spend and the configured daily limit (0 = none).
func (ct *CostTracker) DailyBudget() (spentUSD, limitUSD float64) {
	if ct == nil {
		return 0, 0
	}
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	return ct.dailySpentUSD, ct.dailyLimitUSD
}
