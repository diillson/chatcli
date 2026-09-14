package cli

import (
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// The memory worker's extraction prompt and a squad worker's loop share
// the provider with the main conversation but not its prefix. Booked in
// the same bucket they read as lost prefixes; in their own lanes they
// count toward the bill and nothing else.
func TestBackgroundAndWorkerLanesStayOutOfPrefixTelemetry(t *testing.T) {
	core, logs := observer.New(zap.DebugLevel)
	ct := NewCostTrackerAt(t.TempDir())
	ct.SetLogger(zap.New(core))
	hooks := 0
	ct.SetRealUsageHook(func(string, string) { hooks++ })

	ct.RecordRealUsage("CLAUDEAI", "claude-sonnet-5", realUsage(500, 0, 20000))
	ct.RecordRealUsage("CLAUDEAI", "claude-sonnet-5", realUsage(300, 20000, 400))
	before := ct.CacheStats()

	// A memory extraction: its own prompt, written whole, read nothing.
	ct.RecordMemoryUsage("CLAUDEAI", "claude-sonnet-5", realUsage(200, 0, 12000))
	// A worker's loop: another prefix again.
	ct.RecordRealUsageIn(LaneWorker, "CLAUDEAI", "claude-sonnet-5", realUsage(100, 0, 9000))

	after := ct.CacheStats()
	if after.Requests != before.Requests || after.Misses != 0 || after.WriteReadRatio != before.WriteReadRatio {
		t.Fatalf("other lanes must not touch the prefix telemetry: before %+v after %+v", before, after)
	}
	if hooks != 2 {
		t.Fatalf("the keep-alive hook belongs to the main conversation only, fired %d times", hooks)
	}
	calls, cost := ct.MemoryStats()
	if calls != 1 || cost <= 0 {
		t.Fatalf("the memory slice must still be booked: calls=%d cost=%.4f", calls, cost)
	}
	if ct.TotalCost() <= 0 || ct.Snapshot().TotalRequests != 4 {
		t.Fatalf("every lane counts toward the bill: %+v", ct.Snapshot())
	}
	if n := len(logs.FilterMessage("usage booked outside the main conversation").All()); n != 2 {
		t.Fatalf("other lanes leave their own log line, got %d", n)
	}
	// The next main request is judged against the last MAIN request, not
	// against the worker's write.
	ct.RecordRealUsage("CLAUDEAI", "claude-sonnet-5", realUsage(300, 20400, 500))
	if s := ct.CacheStats(); s.Misses != 0 {
		t.Fatalf("a normal main request after background traffic is not a miss: %+v", s)
	}
	var none *CostTracker
	none.logLaneUsage(LaneBackground, "x", "y", realUsage(1, 1, 1))
}

// The orchestrator reports its own turns and tool calls, so the skill
// ledger and /agents no longer see a run stuck at zero.
func TestRunProgressIsCountedForTheOrchestrator(t *testing.T) {
	a := &AgentMode{logger: zap.NewNop()}
	a.noteRunTurn(1, 10)
	a.noteRunToolCalls(3)
	a.noteRunTurn(2, 10)
	a.noteRunToolCalls(0)
	a.noteRunToolCalls(2)
	if a.runTurns != 2 || a.runToolCalls != 5 {
		t.Fatalf("turns=%d tool calls=%d", a.runTurns, a.runToolCalls)
	}
	a.resetPerRunState()
	if a.runTurns != 0 || a.runToolCalls != 0 {
		t.Fatal("a new run starts from zero")
	}
	var none *AgentMode
	none.noteRunTurn(1, 1)
	none.noteRunToolCalls(1)
}
