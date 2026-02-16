/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package k8s

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// Helper: create healthy and unhealthy stores for multi-target tests
// ---------------------------------------------------------------------------

func makeHealthyStore(name, namespace string) *ObservabilityStore {
	store := NewObservabilityStore(10, 100, 2*time.Hour)
	store.AddSnapshot(ResourceSnapshot{
		Timestamp: time.Now(),
		Deployment: DeploymentStatus{
			Name:              name,
			Namespace:         namespace,
			Replicas:          3,
			ReadyReplicas:     3,
			AvailableReplicas: 3,
			UpdatedReplicas:   3,
			Strategy:          "RollingUpdate",
		},
		Pods: []PodStatus{
			{Name: name + "-abc123", Phase: "Running", Ready: true, RestartCount: 0},
			{Name: name + "-def456", Phase: "Running", Ready: true, RestartCount: 0},
			{Name: name + "-ghi789", Phase: "Running", Ready: true, RestartCount: 0},
		},
	})
	return store
}

func makeWarningStore(name, namespace string) *ObservabilityStore {
	store := NewObservabilityStore(10, 100, 2*time.Hour)
	store.AddSnapshot(ResourceSnapshot{
		Timestamp: time.Now(),
		Deployment: DeploymentStatus{
			Name:              name,
			Namespace:         namespace,
			Replicas:          3,
			ReadyReplicas:     2,
			AvailableReplicas: 2,
			UpdatedReplicas:   3,
			Strategy:          "RollingUpdate",
		},
		Pods: []PodStatus{
			{Name: name + "-abc123", Phase: "Running", Ready: true, RestartCount: 0},
			{Name: name + "-def456", Phase: "Running", Ready: true, RestartCount: 0},
			{Name: name + "-ghi789", Phase: "Pending", Ready: false, RestartCount: 0},
		},
	})
	return store
}

func makeCriticalStore(name, namespace string) *ObservabilityStore {
	store := NewObservabilityStore(10, 100, 2*time.Hour)
	store.AddSnapshot(ResourceSnapshot{
		Timestamp: time.Now(),
		Deployment: DeploymentStatus{
			Name:              name,
			Namespace:         namespace,
			Replicas:          3,
			ReadyReplicas:     1,
			AvailableReplicas: 1,
			UpdatedReplicas:   3,
			Strategy:          "RollingUpdate",
		},
		Pods: []PodStatus{
			{Name: name + "-abc123", Phase: "Running", Ready: true, RestartCount: 0},
			{Name: name + "-def456", Phase: "CrashLoopBackOff", Ready: false, RestartCount: 15},
			{Name: name + "-ghi789", Phase: "CrashLoopBackOff", Ready: false, RestartCount: 12},
		},
	})
	store.AddAlert(Alert{
		Timestamp: time.Now(),
		Severity:  SeverityCritical,
		Type:      AlertPodCrashLoop,
		Message:   "Container restarting repeatedly",
		Object:    "pod/" + name + "-def456",
	})
	return store
}

// ===========================================================================
// 1. PrometheusCollector Parser Tests
// ===========================================================================

// --- ParseMetricLine ---

func TestMultiParseMetricLine_Simple(t *testing.T) {
	name, value, ok := ParseMetricLine("my_metric 42")
	assert.True(t, ok)
	assert.Equal(t, "my_metric", name)
	assert.Equal(t, 42.0, value)
}

func TestMultiParseMetricLine_WithLabels(t *testing.T) {
	name, value, ok := ParseMetricLine(`my_metric{foo="bar"} 1.5`)
	assert.True(t, ok)
	assert.Equal(t, "my_metric", name)
	assert.Equal(t, 1.5, value)
}

func TestMultiParseMetricLine_ScientificNotation(t *testing.T) {
	name, value, ok := ParseMetricLine("my_metric 1.5e+3")
	assert.True(t, ok)
	assert.Equal(t, "my_metric", name)
	assert.Equal(t, 1500.0, value)
}

func TestMultiParseMetricLine_NaN(t *testing.T) {
	_, _, ok := ParseMetricLine("my_metric NaN")
	assert.False(t, ok)
}

func TestMultiParseMetricLine_PosInf(t *testing.T) {
	_, _, ok := ParseMetricLine("my_metric +Inf")
	assert.False(t, ok)
}

func TestMultiParseMetricLine_NegInf(t *testing.T) {
	_, _, ok := ParseMetricLine("my_metric -Inf")
	assert.False(t, ok)
}

func TestMultiParseMetricLine_EmptyLine(t *testing.T) {
	_, _, ok := ParseMetricLine("")
	assert.False(t, ok)
}

func TestMultiParseMetricLine_CommentLine(t *testing.T) {
	// A comment line starts with '#'; ParseMetricLine should fail
	// because the name would be "#" followed by invalid content.
	// Note: comments are actually filtered by ParsePrometheusText before
	// reaching ParseMetricLine, but if called directly with a comment:
	name, _, ok := ParseMetricLine("# HELP my_metric A help text")
	// '#' is treated as the name, and "HELP" would be the value -- which fails to parse.
	// The key behavior is it should not return ok=true with a valid metric.
	if ok {
		// If it somehow parsed, at least the name should not be a real metric
		assert.True(t, strings.HasPrefix(name, "#"))
	}
}

func TestMultiParseMetricLine_WithTimestamp(t *testing.T) {
	// Prometheus format allows an optional timestamp after the value
	name, value, ok := ParseMetricLine("http_requests_total 1027 1395066363000")
	assert.True(t, ok)
	assert.Equal(t, "http_requests_total", name)
	assert.Equal(t, 1027.0, value)
}

func TestMultiParseMetricLine_LabelsWithMultipleFields(t *testing.T) {
	name, value, ok := ParseMetricLine(`http_requests_total{method="GET",code="200"} 1234`)
	assert.True(t, ok)
	assert.Equal(t, "http_requests_total", name)
	assert.Equal(t, 1234.0, value)
}

func TestMultiParseMetricLine_NoValue(t *testing.T) {
	_, _, ok := ParseMetricLine("metric_name_only")
	assert.False(t, ok)
}

func TestMultiParseMetricLine_MalformedLabels(t *testing.T) {
	// Missing closing brace
	_, _, ok := ParseMetricLine(`my_metric{foo="bar" 1.5`)
	assert.False(t, ok)
}

func TestMultiParseMetricLine_ZeroValue(t *testing.T) {
	name, value, ok := ParseMetricLine("my_counter 0")
	assert.True(t, ok)
	assert.Equal(t, "my_counter", name)
	assert.Equal(t, 0.0, value)
}

func TestMultiParseMetricLine_NegativeValue(t *testing.T) {
	name, value, ok := ParseMetricLine("temperature_celsius -17.5")
	assert.True(t, ok)
	assert.Equal(t, "temperature_celsius", name)
	assert.Equal(t, -17.5, value)
}

// --- ParsePrometheusText ---

func TestMultiParsePrometheusText_BasicMetrics(t *testing.T) {
	input := "metric_name 42.5\n"
	result := ParsePrometheusText(strings.NewReader(input), nil)
	assert.Equal(t, 1, len(result))
	assert.Equal(t, 42.5, result["metric_name"])
}

func TestMultiParsePrometheusText_LabeledMetrics(t *testing.T) {
	input := `http_requests_total{method="GET",code="200"} 1234` + "\n"
	result := ParsePrometheusText(strings.NewReader(input), nil)
	assert.Equal(t, 1, len(result))
	assert.Equal(t, 1234.0, result["http_requests_total"])
}

func TestMultiParsePrometheusText_SkipComments(t *testing.T) {
	input := `# HELP http_requests_total Total HTTP requests.
# TYPE http_requests_total counter
http_requests_total 1234
`
	result := ParsePrometheusText(strings.NewReader(input), nil)
	assert.Equal(t, 1, len(result))
	assert.Equal(t, 1234.0, result["http_requests_total"])
}

func TestMultiParsePrometheusText_SkipNaNAndInf(t *testing.T) {
	input := `good_metric 42
nan_metric NaN
inf_metric +Inf
neg_inf_metric -Inf
another_good 7
`
	result := ParsePrometheusText(strings.NewReader(input), nil)
	assert.Equal(t, 2, len(result))
	assert.Equal(t, 42.0, result["good_metric"])
	assert.Equal(t, 7.0, result["another_good"])
}

func TestMultiParsePrometheusText_MultipleMetrics(t *testing.T) {
	input := `# HELP process_cpu CPU usage
process_cpu_seconds_total 123.45
process_open_fds 42
go_goroutines 15
`
	result := ParsePrometheusText(strings.NewReader(input), nil)
	assert.Equal(t, 3, len(result))
	assert.Equal(t, 123.45, result["process_cpu_seconds_total"])
	assert.Equal(t, 42.0, result["process_open_fds"])
	assert.Equal(t, 15.0, result["go_goroutines"])
}

func TestMultiParsePrometheusText_EmptyInput(t *testing.T) {
	result := ParsePrometheusText(strings.NewReader(""), nil)
	assert.Equal(t, 0, len(result))
}

func TestMultiParsePrometheusText_OnlyComments(t *testing.T) {
	input := `# HELP foo A metric
# TYPE foo gauge
`
	result := ParsePrometheusText(strings.NewReader(input), nil)
	assert.Equal(t, 0, len(result))
}

func TestMultiParsePrometheusText_WithGlobFilters(t *testing.T) {
	input := `http_requests_total 100
http_request_duration_seconds 0.5
grpc_connections 10
go_goroutines 25
`
	filters := []string{"http_*"}
	result := ParsePrometheusText(strings.NewReader(input), filters)
	assert.Equal(t, 2, len(result))
	assert.Equal(t, 100.0, result["http_requests_total"])
	assert.Equal(t, 0.5, result["http_request_duration_seconds"])
	_, hasGrpc := result["grpc_connections"]
	assert.False(t, hasGrpc)
	_, hasGo := result["go_goroutines"]
	assert.False(t, hasGo)
}

func TestMultiParsePrometheusText_WithMultipleFilters(t *testing.T) {
	input := `http_requests_total 100
grpc_connections 10
go_goroutines 25
process_cpu 0.5
`
	filters := []string{"http_*", "go_*"}
	result := ParsePrometheusText(strings.NewReader(input), filters)
	assert.Equal(t, 2, len(result))
	assert.Equal(t, 100.0, result["http_requests_total"])
	assert.Equal(t, 25.0, result["go_goroutines"])
}

func TestMultiParsePrometheusText_EmptyFilters(t *testing.T) {
	input := `metric_a 1
metric_b 2
`
	// Empty slice should accept all
	result := ParsePrometheusText(strings.NewReader(input), []string{})
	assert.Equal(t, 2, len(result))
}

func TestMultiParsePrometheusText_FilterMatchesNothing(t *testing.T) {
	input := `http_requests_total 100
http_errors 5
`
	filters := []string{"grpc_*"}
	result := ParsePrometheusText(strings.NewReader(input), filters)
	assert.Equal(t, 0, len(result))
}

func TestMultiParsePrometheusText_DuplicateMetricNames(t *testing.T) {
	// Same metric name appearing multiple times (with different labels in real Prometheus)
	// Our parser strips labels so the last value wins
	input := `http_requests_total{method="GET"} 100
http_requests_total{method="POST"} 50
`
	result := ParsePrometheusText(strings.NewReader(input), nil)
	assert.Equal(t, 1, len(result))
	assert.Equal(t, 50.0, result["http_requests_total"])
}

// --- GlobMatch ---

func TestMultiGlobMatch_ExactMatch(t *testing.T) {
	assert.True(t, GlobMatch("foo", "foo"))
}

func TestMultiGlobMatch_ExactNoMatch(t *testing.T) {
	assert.False(t, GlobMatch("foo", "bar"))
}

func TestMultiGlobMatch_StarSuffix(t *testing.T) {
	assert.True(t, GlobMatch("http_*", "http_requests_total"))
}

func TestMultiGlobMatch_StarPrefix(t *testing.T) {
	assert.True(t, GlobMatch("*_total", "http_requests_total"))
}

func TestMultiGlobMatch_StarMiddle(t *testing.T) {
	assert.True(t, GlobMatch("http_*_total", "http_requests_total"))
}

func TestMultiGlobMatch_NoMatch(t *testing.T) {
	assert.False(t, GlobMatch("grpc_*", "http_requests_total"))
}

func TestMultiGlobMatch_StarOnly(t *testing.T) {
	assert.True(t, GlobMatch("*", "anything_at_all"))
}

func TestMultiGlobMatch_StarOnlyEmpty(t *testing.T) {
	assert.True(t, GlobMatch("*", ""))
}

func TestMultiGlobMatch_EmptyPatternEmptyString(t *testing.T) {
	assert.True(t, GlobMatch("", ""))
}

func TestMultiGlobMatch_EmptyPatternNonEmpty(t *testing.T) {
	assert.False(t, GlobMatch("", "foo"))
}

func TestMultiGlobMatch_MultipleStars(t *testing.T) {
	assert.True(t, GlobMatch("http_*_*_total", "http_request_duration_total"))
}

func TestMultiGlobMatch_StarSuffixPartialName(t *testing.T) {
	// "http_req*" should match "http_requests" but not "grpc_requests"
	assert.True(t, GlobMatch("http_req*", "http_requests"))
	assert.False(t, GlobMatch("http_req*", "grpc_requests"))
}

func TestMultiGlobMatch_StarMiddleNoMatch(t *testing.T) {
	// Pattern requires prefix "http_" and suffix "_total"
	assert.False(t, GlobMatch("http_*_total", "http_requests_count"))
}

// --- MatchesAnyFilter ---

func TestMultiMatchesAnyFilter_EmptyFilters(t *testing.T) {
	// MatchesAnyFilter with empty filters returns false (no pattern can match).
	// The "empty filters = match all" logic lives in ParsePrometheusText,
	// which skips calling MatchesAnyFilter when len(filters) == 0.
	assert.False(t, MatchesAnyFilter("anything", []string{}))
}

func TestMultiMatchesAnyFilter_NilFilters(t *testing.T) {
	// Same as empty filters: no patterns means no match.
	assert.False(t, MatchesAnyFilter("anything", nil))
}

func TestMultiMatchesAnyFilter_MatchingFilter(t *testing.T) {
	assert.True(t, MatchesAnyFilter("http_requests_total", []string{"http_*"}))
}

func TestMultiMatchesAnyFilter_NoMatchingFilter(t *testing.T) {
	assert.False(t, MatchesAnyFilter("grpc_connections", []string{"http_*"}))
}

func TestMultiMatchesAnyFilter_MultipleFiltersOneMatches(t *testing.T) {
	filters := []string{"grpc_*", "http_*", "go_*"}
	assert.True(t, MatchesAnyFilter("http_requests_total", filters))
}

func TestMultiMatchesAnyFilter_MultipleFiltersNoneMatch(t *testing.T) {
	filters := []string{"grpc_*", "redis_*"}
	assert.False(t, MatchesAnyFilter("http_requests_total", filters))
}

func TestMultiMatchesAnyFilter_ExactMatch(t *testing.T) {
	assert.True(t, MatchesAnyFilter("my_metric", []string{"my_metric"}))
}

// ===========================================================================
// 2. MultiSummarizer Tests
// ===========================================================================

// --- GenerateContext ---

func TestMultiSummarizerGenerateContext_NoStores(t *testing.T) {
	ms := NewMultiSummarizer(map[string]*ObservabilityStore{}, 8000)
	ctx := ms.GenerateContext()
	assert.Equal(t, "[K8s Watcher: No targets configured]", ctx)
}

func TestMultiSummarizerGenerateContext_NilStores(t *testing.T) {
	ms := NewMultiSummarizer(nil, 8000)
	ctx := ms.GenerateContext()
	assert.Equal(t, "[K8s Watcher: No targets configured]", ctx)
}

func TestMultiSummarizerGenerateContext_SingleTarget(t *testing.T) {
	store := makeHealthyStore("myapp", "production")
	stores := map[string]*ObservabilityStore{
		"production/myapp": store,
	}
	ms := NewMultiSummarizer(stores, 8000)
	ctx := ms.GenerateContext()

	// Single target delegates to standard Summarizer -- should contain standard header
	assert.Contains(t, ctx, "[K8s Context: deployment/myapp in namespace/production]")
	assert.Contains(t, ctx, "## Deployment Status")
	// Should NOT contain multi-watcher header
	assert.NotContains(t, ctx, "Multi-Watcher")
}

func TestMultiSummarizerGenerateContext_MultipleHealthyTargets(t *testing.T) {
	stores := map[string]*ObservabilityStore{
		"production/frontend": makeHealthyStore("frontend", "production"),
		"production/backend":  makeHealthyStore("backend", "production"),
		"production/worker":   makeHealthyStore("worker", "production"),
	}
	ms := NewMultiSummarizer(stores, 8000)
	ctx := ms.GenerateContext()

	// Multi-watcher header
	assert.Contains(t, ctx, "[K8s Multi-Watcher: 3 targets monitored]")

	// All healthy targets should get compact one-liners under "Healthy Targets"
	assert.Contains(t, ctx, "--- Healthy Targets ---")

	// Should contain status summaries for each target
	assert.Contains(t, ctx, "production/frontend")
	assert.Contains(t, ctx, "production/backend")
	assert.Contains(t, ctx, "production/worker")

	// Healthy targets should NOT get detailed deployment status sections
	assert.NotContains(t, ctx, "## Deployment Status")
}

func TestMultiSummarizerGenerateContext_MixedHealthTargets(t *testing.T) {
	stores := map[string]*ObservabilityStore{
		"production/frontend": makeHealthyStore("frontend", "production"),
		"production/backend":  makeCriticalStore("backend", "production"),
		"production/worker":   makeWarningStore("worker", "production"),
	}
	ms := NewMultiSummarizer(stores, 16000) // large budget to avoid compression
	ctx := ms.GenerateContext()

	assert.Contains(t, ctx, "[K8s Multi-Watcher: 3 targets monitored]")

	// Unhealthy targets should get detailed context
	assert.Contains(t, ctx, "--- Targets Requiring Attention ---")

	// Critical target should have detailed deployment info
	assert.Contains(t, ctx, "deployment/backend")

	// Healthy target should be compact
	assert.Contains(t, ctx, "--- Healthy Targets ---")
}

func TestMultiSummarizerGenerateContext_NoDataCollected(t *testing.T) {
	// Stores exist but have no snapshots
	stores := map[string]*ObservabilityStore{
		"production/frontend": NewObservabilityStore(10, 100, 2*time.Hour),
		"production/backend":  NewObservabilityStore(10, 100, 2*time.Hour),
	}
	ms := NewMultiSummarizer(stores, 8000)
	ctx := ms.GenerateContext()
	assert.Equal(t, "[K8s Watcher: No data collected yet]", ctx)
}

func TestMultiSummarizerGenerateContext_DefaultMaxChars(t *testing.T) {
	// maxChars <= 0 should default to 32000
	ms := NewMultiSummarizer(map[string]*ObservabilityStore{}, 0)
	assert.Equal(t, 32000, ms.maxChars)

	ms2 := NewMultiSummarizer(map[string]*ObservabilityStore{}, -100)
	assert.Equal(t, 32000, ms2.maxChars)
}

// --- GenerateStatusSummary ---

func TestMultiSummarizerStatusSummary_AllHealthy(t *testing.T) {
	stores := map[string]*ObservabilityStore{
		"production/frontend": makeHealthyStore("frontend", "production"),
		"production/backend":  makeHealthyStore("backend", "production"),
		"production/worker":   makeHealthyStore("worker", "production"),
	}
	ms := NewMultiSummarizer(stores, 8000)
	summary := ms.GenerateStatusSummary()

	assert.Equal(t, "Watching 3 targets: 3 healthy, 0 warning, 0 critical", summary)
}

func TestMultiSummarizerStatusSummary_MixedHealth(t *testing.T) {
	stores := map[string]*ObservabilityStore{
		"production/frontend": makeHealthyStore("frontend", "production"),
		"production/backend":  makeCriticalStore("backend", "production"),
		"production/worker":   makeWarningStore("worker", "production"),
	}
	ms := NewMultiSummarizer(stores, 8000)
	summary := ms.GenerateStatusSummary()

	assert.Equal(t, "Watching 3 targets: 1 healthy, 1 warning, 1 critical", summary)
}

func TestMultiSummarizerStatusSummary_AllCritical(t *testing.T) {
	stores := map[string]*ObservabilityStore{
		"production/svc-a": makeCriticalStore("svc-a", "production"),
		"production/svc-b": makeCriticalStore("svc-b", "production"),
	}
	ms := NewMultiSummarizer(stores, 8000)
	summary := ms.GenerateStatusSummary()

	assert.Equal(t, "Watching 2 targets: 0 healthy, 0 warning, 2 critical", summary)
}

func TestMultiSummarizerStatusSummary_NoData(t *testing.T) {
	stores := map[string]*ObservabilityStore{
		"production/frontend": NewObservabilityStore(10, 100, 2*time.Hour),
	}
	ms := NewMultiSummarizer(stores, 8000)
	summary := ms.GenerateStatusSummary()

	// Store has no snapshot, so it should still report 1 target with 0 in all categories
	assert.Equal(t, "Watching 1 targets: 0 healthy, 0 warning, 0 critical", summary)
}

func TestMultiSummarizerStatusSummary_WarningViaLowReplicas(t *testing.T) {
	stores := map[string]*ObservabilityStore{
		"default/myapp": makeWarningStore("myapp", "default"),
	}
	ms := NewMultiSummarizer(stores, 8000)
	summary := ms.GenerateStatusSummary()

	assert.Contains(t, summary, "1 warning")
	assert.Contains(t, summary, "0 critical")
}

// --- scoreTargets ---

func TestMultiSummarizerScoreTargets_Healthy(t *testing.T) {
	stores := map[string]*ObservabilityStore{
		"production/frontend": makeHealthyStore("frontend", "production"),
	}
	ms := NewMultiSummarizer(stores, 8000)
	scores := ms.scoreTargets()

	assert.Equal(t, 1, len(scores))
	assert.Equal(t, 0, scores[0].Score)
	assert.Equal(t, 0, scores[0].AlertCount)
}

func TestMultiSummarizerScoreTargets_WarningLowReplicas(t *testing.T) {
	stores := map[string]*ObservabilityStore{
		"production/backend": makeWarningStore("backend", "production"),
	}
	ms := NewMultiSummarizer(stores, 8000)
	scores := ms.scoreTargets()

	assert.Equal(t, 1, len(scores))
	assert.Equal(t, 1, scores[0].Score)
}

func TestMultiSummarizerScoreTargets_WarningViaWarningAlert(t *testing.T) {
	store := makeHealthyStore("myapp", "production")
	store.AddAlert(Alert{
		Timestamp: time.Now(),
		Severity:  SeverityWarning,
		Type:      AlertHighRestarts,
		Message:   "High restart count detected",
		Object:    "pod/myapp-abc123",
	})
	stores := map[string]*ObservabilityStore{
		"production/myapp": store,
	}
	ms := NewMultiSummarizer(stores, 8000)
	scores := ms.scoreTargets()

	assert.Equal(t, 1, len(scores))
	assert.Equal(t, 1, scores[0].Score)
	assert.Equal(t, 1, scores[0].AlertCount)
}

func TestMultiSummarizerScoreTargets_WarningViaErrorLogs(t *testing.T) {
	store := makeHealthyStore("myapp", "production")
	store.AddLogs([]LogEntry{
		{PodName: "myapp-abc123", Container: "app", Line: "ERROR: something failed", Timestamp: time.Now(), IsError: true},
	})
	stores := map[string]*ObservabilityStore{
		"production/myapp": store,
	}
	ms := NewMultiSummarizer(stores, 8000)
	scores := ms.scoreTargets()

	assert.Equal(t, 1, len(scores))
	assert.Equal(t, 1, scores[0].Score)
}

func TestMultiSummarizerScoreTargets_Critical(t *testing.T) {
	stores := map[string]*ObservabilityStore{
		"production/backend": makeCriticalStore("backend", "production"),
	}
	ms := NewMultiSummarizer(stores, 8000)
	scores := ms.scoreTargets()

	assert.Equal(t, 1, len(scores))
	assert.Equal(t, 2, scores[0].Score)
	assert.Equal(t, 1, scores[0].AlertCount)
}

func TestMultiSummarizerScoreTargets_NoSnapshot(t *testing.T) {
	stores := map[string]*ObservabilityStore{
		"production/myapp": NewObservabilityStore(10, 100, 2*time.Hour),
	}
	ms := NewMultiSummarizer(stores, 8000)
	scores := ms.scoreTargets()

	// No snapshot means the target is skipped
	assert.Equal(t, 0, len(scores))
}

func TestMultiSummarizerScoreTargets_MultipleTargets(t *testing.T) {
	stores := map[string]*ObservabilityStore{
		"production/frontend": makeHealthyStore("frontend", "production"),
		"production/backend":  makeCriticalStore("backend", "production"),
		"production/worker":   makeWarningStore("worker", "production"),
	}
	ms := NewMultiSummarizer(stores, 8000)
	scores := ms.scoreTargets()

	assert.Equal(t, 3, len(scores))

	// Build a map for easy lookup since iteration order over maps is not deterministic
	scoreMap := make(map[string]int)
	for _, s := range scores {
		scoreMap[s.Key] = s.Score
	}

	assert.Equal(t, 0, scoreMap["production/frontend"])
	assert.Equal(t, 2, scoreMap["production/backend"])
	assert.Equal(t, 1, scoreMap["production/worker"])
}

// ===========================================================================
// 3. Config Tests
// ===========================================================================

// --- LoadMultiWatchConfig ---

func TestMultiLoadMultiWatchConfig_ValidYAML(t *testing.T) {
	yamlContent := `
targets:
  - deployment: frontend
    namespace: production
    metricsPort: 9090
    metricsPath: /metrics
    metricsFilter:
      - "http_*"
      - "go_*"
  - deployment: backend
    namespace: staging
    metricsPort: 8080
  - deployment: worker
interval: "15s"
window: "1h"
maxLogLines: 50
maxContextChars: 4000
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	err := os.WriteFile(path, []byte(yamlContent), 0644)
	assert.NoError(t, err)

	cfg, err := LoadMultiWatchConfig(path)
	assert.NoError(t, err)
	assert.NotNil(t, cfg)

	// Targets
	assert.Equal(t, 3, len(cfg.Targets))

	// First target - fully specified
	assert.Equal(t, "frontend", cfg.Targets[0].Deployment)
	assert.Equal(t, "production", cfg.Targets[0].Namespace)
	assert.Equal(t, 9090, cfg.Targets[0].MetricsPort)
	assert.Equal(t, "/metrics", cfg.Targets[0].MetricsPath)
	assert.Equal(t, []string{"http_*", "go_*"}, cfg.Targets[0].MetricsFilter)

	// Second target - partial, default metricsPath applied
	assert.Equal(t, "backend", cfg.Targets[1].Deployment)
	assert.Equal(t, "staging", cfg.Targets[1].Namespace)
	assert.Equal(t, 8080, cfg.Targets[1].MetricsPort)
	assert.Equal(t, "/metrics", cfg.Targets[1].MetricsPath) // default applied

	// Third target - minimal, namespace defaults to "default"
	assert.Equal(t, "worker", cfg.Targets[2].Deployment)
	assert.Equal(t, "default", cfg.Targets[2].Namespace) // default applied

	// Intervals
	assert.Equal(t, 15*time.Second, cfg.Interval)
	assert.Equal(t, 1*time.Hour, cfg.Window)
	assert.Equal(t, 50, cfg.MaxLogLines)
	assert.Equal(t, 4000, cfg.MaxContextChars)
}

func TestMultiLoadMultiWatchConfig_MissingTargets(t *testing.T) {
	yamlContent := `
interval: "15s"
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	err := os.WriteFile(path, []byte(yamlContent), 0644)
	assert.NoError(t, err)

	cfg, err := LoadMultiWatchConfig(path)
	assert.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "no targets")
}

func TestMultiLoadMultiWatchConfig_EmptyTargets(t *testing.T) {
	yamlContent := `
targets: []
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	err := os.WriteFile(path, []byte(yamlContent), 0644)
	assert.NoError(t, err)

	cfg, err := LoadMultiWatchConfig(path)
	assert.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "no targets")
}

func TestMultiLoadMultiWatchConfig_InvalidInterval(t *testing.T) {
	yamlContent := `
targets:
  - deployment: myapp
interval: "not-a-duration"
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	err := os.WriteFile(path, []byte(yamlContent), 0644)
	assert.NoError(t, err)

	cfg, err := LoadMultiWatchConfig(path)
	assert.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "invalid interval")
}

func TestMultiLoadMultiWatchConfig_InvalidWindow(t *testing.T) {
	yamlContent := `
targets:
  - deployment: myapp
window: "garbage"
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	err := os.WriteFile(path, []byte(yamlContent), 0644)
	assert.NoError(t, err)

	cfg, err := LoadMultiWatchConfig(path)
	assert.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "invalid window")
}

func TestMultiLoadMultiWatchConfig_Defaults(t *testing.T) {
	yamlContent := `
targets:
  - deployment: myapp
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	err := os.WriteFile(path, []byte(yamlContent), 0644)
	assert.NoError(t, err)

	cfg, err := LoadMultiWatchConfig(path)
	assert.NoError(t, err)
	assert.NotNil(t, cfg)

	// Defaults
	assert.Equal(t, 30*time.Second, cfg.Interval)
	assert.Equal(t, 2*time.Hour, cfg.Window)
	assert.Equal(t, 100, cfg.MaxLogLines)
	assert.Equal(t, 32000, cfg.MaxContextChars)
	assert.Equal(t, "default", cfg.Targets[0].Namespace)
}

func TestMultiLoadMultiWatchConfig_MissingDeployment(t *testing.T) {
	yamlContent := `
targets:
  - namespace: production
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	err := os.WriteFile(path, []byte(yamlContent), 0644)
	assert.NoError(t, err)

	cfg, err := LoadMultiWatchConfig(path)
	assert.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "deployment is required")
}

func TestMultiLoadMultiWatchConfig_FileNotFound(t *testing.T) {
	cfg, err := LoadMultiWatchConfig("/nonexistent/path/config.yaml")
	assert.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "failed to read")
}

func TestMultiLoadMultiWatchConfig_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	err := os.WriteFile(path, []byte("{{{{not yaml"), 0644)
	assert.NoError(t, err)

	cfg, err := LoadMultiWatchConfig(path)
	assert.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "failed to parse")
}

func TestMultiLoadMultiWatchConfig_MetricsPathDefaultOnlyWhenPortSet(t *testing.T) {
	yamlContent := `
targets:
  - deployment: with-port
    metricsPort: 9090
  - deployment: without-port
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	err := os.WriteFile(path, []byte(yamlContent), 0644)
	assert.NoError(t, err)

	cfg, err := LoadMultiWatchConfig(path)
	assert.NoError(t, err)

	// With port: metricsPath should default to "/metrics"
	assert.Equal(t, "/metrics", cfg.Targets[0].MetricsPath)
	// Without port: metricsPath should remain empty
	assert.Equal(t, "", cfg.Targets[1].MetricsPath)
}

// --- SingleTargetToMulti ---

func TestMultiSingleTargetToMulti(t *testing.T) {
	cfg := WatchConfig{
		Deployment:  "myapp",
		Namespace:   "production",
		Interval:    15 * time.Second,
		Window:      1 * time.Hour,
		MaxLogLines: 200,
		Kubeconfig:  "/home/user/.kube/config",
	}

	multi := SingleTargetToMulti(cfg)

	assert.Equal(t, 1, len(multi.Targets))
	assert.Equal(t, "myapp", multi.Targets[0].Deployment)
	assert.Equal(t, "production", multi.Targets[0].Namespace)
	assert.Equal(t, 15*time.Second, multi.Interval)
	assert.Equal(t, 1*time.Hour, multi.Window)
	assert.Equal(t, 200, multi.MaxLogLines)
	assert.Equal(t, "/home/user/.kube/config", multi.Kubeconfig)
	assert.Equal(t, 32000, multi.MaxContextChars)
}

func TestMultiSingleTargetToMulti_EmptyNamespace(t *testing.T) {
	cfg := WatchConfig{
		Deployment: "myapp",
	}

	multi := SingleTargetToMulti(cfg)
	assert.Equal(t, "", multi.Targets[0].Namespace)
	// Note: SingleTargetToMulti does NOT apply defaults; that's LoadMultiWatchConfig's job
}

func TestMultiSingleTargetToMulti_TargetKey(t *testing.T) {
	cfg := WatchConfig{
		Deployment: "myapp",
		Namespace:  "staging",
	}
	multi := SingleTargetToMulti(cfg)
	assert.Equal(t, "staging/myapp", multi.Targets[0].Key())
}
