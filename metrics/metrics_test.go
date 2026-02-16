package metrics

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestRegistryContainsGoAndProcessCollectors(t *testing.T) {
	// The default Registry should include Go and Process collectors
	families, err := Registry.Gather()
	if err != nil {
		t.Fatalf("failed to gather: %v", err)
	}

	names := make(map[string]bool)
	for _, f := range families {
		names[f.GetName()] = true
	}

	if !names["go_goroutines"] {
		t.Error("expected go_goroutines metric from GoCollector")
	}
	if !names["process_cpu_seconds_total"] {
		t.Error("expected process_cpu_seconds_total from ProcessCollector")
	}
}

func TestGRPCMetricsRegistered(t *testing.T) {
	// Create a fresh registry to avoid conflicts with package-level init
	reg := newTestRegistry()
	m := newGRPCMetricsOn(reg)

	// Initialize metrics with at least one label set so they appear in Gather
	m.RequestsTotal.WithLabelValues("/test.Method", "OK").Inc()
	m.RequestDuration.WithLabelValues("/test.Method").Observe(0.5)
	m.InFlightRequests.WithLabelValues("/test.Method").Set(0)
	m.StreamMsgsSent.WithLabelValues("/test.Method").Inc()
	m.StreamMsgsReceived.WithLabelValues("/test.Method").Inc()

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("failed to gather: %v", err)
	}

	names := make(map[string]bool)
	for _, f := range families {
		names[f.GetName()] = true
	}

	expected := []string{
		"chatcli_grpc_requests_total",
		"chatcli_grpc_request_duration_seconds",
		"chatcli_grpc_in_flight_requests",
		"chatcli_grpc_stream_messages_sent_total",
		"chatcli_grpc_stream_messages_received_total",
	}

	for _, name := range expected {
		if !names[name] {
			t.Errorf("expected metric %q not found", name)
		}
	}
}

func TestLLMMetricsRegistered(t *testing.T) {
	reg := newTestRegistry()
	m := newLLMMetricsOn(reg)

	// Initialize metrics with at least one label set
	m.RequestsTotal.WithLabelValues("OPENAI", "gpt-4", "success").Inc()
	m.RequestDuration.WithLabelValues("OPENAI", "gpt-4").Observe(1.0)
	m.TokensUsed.WithLabelValues("OPENAI", "gpt-4", "prompt").Add(100)
	m.ErrorsTotal.WithLabelValues("OPENAI", "gpt-4", "rate_limit").Inc()

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("failed to gather: %v", err)
	}

	names := make(map[string]bool)
	for _, f := range families {
		names[f.GetName()] = true
	}

	expected := []string{
		"chatcli_llm_requests_total",
		"chatcli_llm_request_duration_seconds",
		"chatcli_llm_tokens_used_total",
		"chatcli_llm_errors_total",
	}

	for _, name := range expected {
		if !names[name] {
			t.Errorf("expected metric %q not found", name)
		}
	}
}

func TestMetricsServerStartStop(t *testing.T) {
	logger := zap.NewNop()
	srv := NewServer(19876, logger)
	srv.Start()

	// Give it time to start
	time.Sleep(100 * time.Millisecond)

	// Test /healthz
	resp, err := http.Get("http://localhost:19876/healthz")
	if err != nil {
		t.Fatalf("failed to reach healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// Test /metrics
	resp2, err := http.Get("http://localhost:19876/metrics")
	if err != nil {
		t.Fatalf("failed to reach metrics: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp2.StatusCode)
	}

	body, _ := io.ReadAll(resp2.Body)
	if !strings.Contains(string(body), "go_goroutines") {
		t.Error("expected go_goroutines in metrics output")
	}

	srv.Stop()
}

func TestWatcherMetricsRecorder(t *testing.T) {
	reg := newTestRegistry()
	m := newWatcherMetricsOn(reg)
	rec := m.Recorder()

	rec.ObserveCollectionDuration("default/myapp", 1.5)
	rec.IncrementCollectionErrors("default/myapp")
	rec.IncrementAlert("default/myapp", "CRITICAL", "OOMKilled")
	rec.SetPodsReady("default", "myapp", 3)
	rec.SetPodsDesired("default", "myapp", 5)
	rec.SetSnapshotsStored("default/myapp", 10)
	rec.SetPodRestarts("default/myapp", 7)

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("failed to gather: %v", err)
	}

	names := make(map[string]bool)
	for _, f := range families {
		names[f.GetName()] = true
	}

	if !names["chatcli_watcher_collection_duration_seconds"] {
		t.Error("expected chatcli_watcher_collection_duration_seconds")
	}
	if !names["chatcli_watcher_collection_errors_total"] {
		t.Error("expected chatcli_watcher_collection_errors_total")
	}
	if !names["chatcli_watcher_alerts_total"] {
		t.Error("expected chatcli_watcher_alerts_total")
	}
	if !names["chatcli_watcher_pods_ready"] {
		t.Error("expected chatcli_watcher_pods_ready")
	}
}
