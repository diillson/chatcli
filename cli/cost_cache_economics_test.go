package cli

import (
	"math"
	"testing"
	"time"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func near(a, b float64) bool { return math.Abs(a-b) < 0.0005 }

// The balance of a measured session: 301.3K reads and 109.1K hour-long
// writes on a $3 input model. Reads alone say $0.81 saved; the write
// premium gives a third of it back.
func TestCacheEconomicsCountsTheWritePremium(t *testing.T) {
	ct := NewCostTrackerAt(t.TempDir())
	ct.RecordRealUsage("CLAUDEAI", "claude-sonnet-4-6", &models.UsageInfo{
		IsReal:                     true,
		PromptTokens:               30,
		CompletionTokens:           4100,
		CacheReadInputTokens:       301300,
		CacheCreationInputTokens:   109100,
		CacheCreation1hInputTokens: 109100,
	})
	ct.mu.RLock()
	e := cacheEconomicsLocked(ct)
	ct.mu.RUnlock()
	if !near(e.Gross, 0.8135) {
		t.Errorf("gross = %.4f, want 0.8135", e.Gross)
	}
	if !near(e.Premium, 0.3273) {
		t.Errorf("premium = %.4f, want 0.3273 (109.1K at 2x minus input)", e.Premium)
	}
	if !near(e.Net, e.Gross-e.Premium) || e.Net > e.Gross {
		t.Errorf("net = %.4f must be gross minus premium", e.Net)
	}
	if !near(e.WithoutCache, e.Actual+e.Net) || e.SavedPct <= 0 || e.SavedPct >= 100 {
		t.Errorf("without cache = %.4f actual = %.4f pct = %.1f", e.WithoutCache, e.Actual, e.SavedPct)
	}
}

// A subset schema charges nothing for the write, so the premium is zero
// and the net saving is the whole read discount.
func TestCacheEconomicsSubsetHasNoPremium(t *testing.T) {
	ct := NewCostTrackerAt(t.TempDir())
	ct.RecordRealUsage("OPENAI", "gpt-5.6", &models.UsageInfo{
		IsReal: true, PromptTokens: 20000, CompletionTokens: 500, CacheReadInputTokens: 18000,
	})
	ct.mu.RLock()
	e := cacheEconomicsLocked(ct)
	ct.mu.RUnlock()
	if e.Premium != 0 || e.Gross <= 0 || !near(e.Net, e.Gross) {
		t.Errorf("subset economics = %+v", e)
	}
}

// A big tail on a stable prefix is not a miss: the request still read
// back everything the previous one left in the cache.
func TestCacheTelemetry_LargeTailIsNotAMiss(t *testing.T) {
	ct := NewCostTracker()
	ct.RecordRealUsage("CLAUDEAI", "claude-sonnet-5", realUsage(500, 0, 20000))
	ct.RecordRealUsage("CLAUDEAI", "claude-sonnet-5", realUsage(300, 20000, 400))
	// A 12K tool result: the write is huge, the read is intact.
	ct.RecordRealUsage("CLAUDEAI", "claude-sonnet-5", realUsage(300, 20400, 12000))
	if s := ct.CacheStats(); s.Misses != 0 || s.Expired != 0 || s.Rebuilds != 0 {
		t.Fatalf("a large tail must not count as a miss: %+v", s)
	}
	// The prefix is lost when the read collapses below what was readable.
	ct.RecordRealUsage("CLAUDEAI", "claude-sonnet-5", realUsage(300, 5000, 28000))
	if s := ct.CacheStats(); s.Misses != 1 {
		t.Fatalf("a collapsed read is a miss: %+v", s)
	}
}

// A prefix lost after a pause the cache did not survive is an expiry, not
// instability: it is counted apart and never arms the streak alert.
func TestCacheTelemetry_ExpiryIsNotInstability(t *testing.T) {
	c := &cacheTelemetry{}
	t0 := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	c.observe("CLAUDEAI", "claude-sonnet-5", realUsage(500, 0, 20000), t0)
	c.observe("CLAUDEAI", "claude-sonnet-5", realUsage(300, 20000, 400), t0.Add(time.Minute))
	obs := c.observe("CLAUDEAI", "claude-sonnet-5", realUsage(300, 0, 21000), t0.Add(12*time.Minute))
	if !obs.Expired || obs.Miss || obs.Rebuild {
		t.Fatalf("a rewrite after a 12 minute pause is an expiry: %+v", obs)
	}
	if c.expired != 1 || c.misses != 0 || c.idleExpiries != 1 || c.alertArmed {
		t.Fatalf("counters = expired %d misses %d idle %d alert %v", c.expired, c.misses, c.idleExpiries, c.alertArmed)
	}
	if obs.Expected != 20400 || obs.IdleGap != 11*time.Minute {
		t.Fatalf("observation must carry what the previous request established: %+v", obs)
	}
}

// Subset schemas are judged against the previous prompt, so a prompt that
// keeps most of the previous one cached is a hit however large it is.
func TestCacheTelemetry_SubsetJudgedAgainstPreviousPrompt(t *testing.T) {
	ct := NewCostTracker()
	ct.RecordRealUsage("OPENAI", "gpt-5.6", realUsage(8000, 7000, 0))
	ct.RecordRealUsage("OPENAI", "gpt-5.6", realUsage(30000, 7900, 0)) // 22K pasted: still a hit
	if s := ct.CacheStats(); s.Misses != 0 {
		t.Fatalf("a large prompt with the previous one cached is not a miss: %+v", s)
	}
	ct.RecordRealUsage("OPENAI", "gpt-5.6", realUsage(30500, 2000, 0)) // lost most of 30K
	if s := ct.CacheStats(); s.Misses != 1 {
		t.Fatalf("a collapsed cached share is a miss: %+v", s)
	}
}

// Every observed request leaves one debug line with the buckets, what was
// expected, the gap and the outcome — the record the counters come from.
func TestCacheObservationIsLoggedPerRequest(t *testing.T) {
	core, logs := observer.New(zap.DebugLevel)
	ct := NewCostTracker()
	ct.SetLogger(zap.New(core))
	ct.RecordRealUsage("CLAUDEAI", "claude-sonnet-5", realUsage(500, 0, 20000))
	ct.RecordRealUsage("CLAUDEAI", "claude-sonnet-5", realUsage(300, 1000, 25000))
	entries := logs.FilterMessage("prompt cache observed").All()
	if len(entries) != 2 {
		t.Fatalf("want one line per observed request, got %d", len(entries))
	}
	last := entries[1].ContextMap()
	if last["outcome"] != "miss" || last["cache_read"] != int64(1000) || last["expected_read"] != int64(20000) {
		t.Fatalf("unexpected fields: %v", last)
	}
	var none *CostTracker
	none.SetLogger(nil)
	none.logCacheObservation(cacheObservation{Observed: true})
}
