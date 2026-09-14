package cli

import (
	"strings"
	"testing"
)

// The cache telemetry rides in the cost snapshot and comes back with the
// session: counters, ratio, hit share and the ttl promotion flag.
func TestCacheTelemetryRoundTripsThroughTheCostSnapshot(t *testing.T) {
	// The default store: RestoreSession reads from it, and the test
	// environment points it at a per-run temporary home.
	ct := NewCostTracker()
	ct.RecordRealUsage("CLAUDEAI", "claude-sonnet-5", realUsage(500, 0, 20000))
	ct.RecordRealUsage("CLAUDEAI", "claude-sonnet-5", realUsage(300, 20000, 400))
	ct.RecordRealUsage("CLAUDEAI", "claude-sonnet-5", realUsage(300, 1000, 25000)) // lost the prefix
	ct.mu.Lock()
	ct.cacheTTLPromoted = true
	ct.mu.Unlock()
	if err := ct.SaveSession(); err != nil {
		t.Fatal(err)
	}
	snap := ct.Snapshot()
	if snap.Cache == nil || snap.Cache.Requests != 3 || snap.Cache.Misses != 1 || !snap.Cache.TTLPromoted {
		t.Fatalf("snapshot cache = %+v", snap.Cache)
	}
	if snap.Cache.WriteReadRatio <= 1 || snap.Cache.HitPct <= 0 {
		t.Fatalf("a session that rewrote its prefix must show a ratio above 1: %+v", snap.Cache)
	}

	restored := NewCostTracker()
	restored.RecordRealUsage("OPENAI", "gpt-5.6", realUsage(9000, 8000, 0)) // must be replaced, not merged
	if err := restored.RestoreSession(snap.SessionID); err != nil {
		t.Fatal(err)
	}
	before, after := ct.CacheStats(), restored.CacheStats()
	if after.Requests != before.Requests || after.Misses != before.Misses || after.Rebuilds != before.Rebuilds ||
		after.Expired != before.Expired || after.WriteReadRatio != before.WriteReadRatio || after.HitPct != before.HitPct {
		t.Fatalf("restored stats differ:\n%+v\n%+v", before, after)
	}
	restored.mu.RLock()
	promoted := restored.cacheTTLPromoted
	restored.mu.RUnlock()
	if !promoted {
		t.Error("the ttl promotion must be restored so the session does not promote twice")
	}
	// A request after the restore is judged as a first one: it can never
	// be counted as a miss on the strength of state that was not kept.
	restored.RecordRealUsage("CLAUDEAI", "claude-sonnet-5", realUsage(300, 0, 30000))
	if s := restored.CacheStats(); s.Misses != before.Misses || s.Requests != before.Requests+1 {
		t.Fatalf("first request after restore must not be a miss: %+v", s)
	}
}

// An older cost file without a cache record resets the telemetry instead
// of leaving another session's counters in place.
func TestRestoreWithoutCacheRecordResetsTelemetry(t *testing.T) {
	c := &cacheTelemetry{requests: 4, misses: 2, byProvider: map[string]*providerCache{"X": {requests: 4}}}
	c.restore(nil)
	if c.requests != 0 || c.misses != 0 || len(c.byProvider) != 0 {
		t.Fatalf("expected a reset, got %+v", c)
	}
	var none *cacheTelemetry
	none.restore(nil)
	if none.snapshot(false) != nil {
		t.Fatal("nil telemetry has no snapshot")
	}
}

// The listing row summarizes a persisted session on prefix stability.
func TestSessionCacheRow(t *testing.T) {
	if sessionCacheRow(nil) != "" || sessionCacheRow(&CacheTelemetrySnapshot{}) != "" {
		t.Fatal("no record, no row")
	}
	row := sessionCacheRow(&CacheTelemetrySnapshot{Requests: 10, HitPct: 73.4, WriteReadRatio: 0.36, Misses: 2, Expired: 1, Rebuilds: 3, TTL: "1h"})
	for _, want := range []string{"73%", "0.36", "1h"} {
		if !strings.Contains(row, want) {
			t.Errorf("row %q lacks %q", row, want)
		}
	}
}

// The ratio is writes over reads and the dashboard metrics carry it.
func TestWriteReadRatioAndMetrics(t *testing.T) {
	if writeReadRatio(50, 0) != 0 || writeReadRatio(50, 100) != 0.5 {
		t.Fatal("ratio arithmetic")
	}
	cli := &ChatCLI{costTracker: NewCostTrackerAt(t.TempDir())}
	cli.costTracker.RecordRealUsage("CLAUDEAI", "claude-sonnet-5", realUsage(500, 20000, 4000))
	found := map[string]bool{}
	for _, m := range cli.telemetryMetrics() {
		found[m.Name] = true
	}
	if !found["chatcli.cache.hit_pct"] || !found["chatcli.cache.write_read_ratio"] {
		t.Fatalf("cache gauges missing from %v", found)
	}

}
