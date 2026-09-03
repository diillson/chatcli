/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package telemetry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestExporter_PushesOTLPJSONWithResourceAndHeaders(t *testing.T) {
	var mu sync.Mutex
	var bodies [][]byte
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, b)
		auth = r.Header.Get("Authorization")
		mu.Unlock()
		if r.URL.Path != "/v1/metrics" || r.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	t.Setenv(EnvEndpoint, srv.URL)
	t.Setenv(EnvMetricsEndpoint, "")
	t.Setenv(EnvHeaders, "Authorization=Bearer%20abc,x-team=ctx")
	t.Setenv(EnvServiceName, "chatcli-test")
	t.Setenv(EnvResourceAttrs, "deployment.environment=staging")
	t.Setenv(EnvExportInterval, "1000")
	source := func() []Metric {
		return []Metric{{Name: "chatcli.llm.tokens", Unit: "{token}", Points: []Point{{Value: 42, Attrs: map[string]string{"kind": "input", "provider": "openai"}}}}, {Name: "empty"}}
	}
	exp := NewFromEnv(source, map[string]string{"chatcli.surface": "repl", "chatcli.tenant": ""})
	if exp == nil {
		t.Fatal("endpoint configured → exporter")
	}
	if err := exp.Push(context.Background()); err != nil {
		t.Fatal(err)
	}
	exp.SetResource("chatcli.tenant", "acme")
	exp.Start(context.Background())
	exp.Start(context.Background()) // idempotent
	exp.Stop(context.Background())  // final push
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) < 2 || auth != "Bearer abc" {
		t.Fatalf("pushes=%d auth=%q", len(bodies), auth)
	}
	var req otlpExportRequest
	if err := json.Unmarshal(bodies[len(bodies)-1], &req); err != nil {
		t.Fatal(err)
	}
	rm := req.ResourceMetrics[0]
	attrs := map[string]string{}
	for _, kv := range rm.Resource.Attributes {
		attrs[kv.Key] = kv.Value.StringValue
	}
	if attrs["service.name"] != "chatcli-test" || attrs["deployment.environment"] != "staging" || attrs["chatcli.surface"] != "repl" || attrs["chatcli.tenant"] != "acme" {
		t.Fatalf("resource = %v", attrs)
	}
	metrics := rm.ScopeMetrics[0].Metrics
	if len(metrics) != 1 || metrics[0].Name != "chatcli.llm.tokens" || metrics[0].Sum.AggregationTemporality != 2 || !metrics[0].Sum.IsMonotonic {
		t.Fatalf("metrics = %+v", metrics)
	}
	dp := metrics[0].Sum.DataPoints[0]
	if dp.AsDouble != 42 || dp.StartTimeUnixNano == "" || dp.TimeUnixNano == "" || len(dp.Attributes) != 2 {
		t.Fatalf("datapoint = %+v", dp)
	}
	if ep, pushes, lastErr := exp.Status(); ep != srv.URL+"/v1/metrics" || pushes < 2 || lastErr != "" {
		t.Fatalf("status = %s %d %q", ep, pushes, lastErr)
	}
}

func TestExporter_EnvResolutionAndFailures(t *testing.T) {
	t.Setenv(EnvEndpoint, "")
	t.Setenv(EnvMetricsEndpoint, "")
	if Enabled() || NewFromEnv(func() []Metric { return nil }, nil) != nil {
		t.Fatal("no endpoint → disabled")
	}
	t.Setenv(EnvMetricsEndpoint, "http://collector:4318/custom/metrics")
	t.Setenv(EnvEndpoint, "http://ignored:4318/")
	if MetricsEndpoint() != "http://collector:4318/custom/metrics" {
		t.Fatal("the signal-specific endpoint wins")
	}
	t.Setenv(EnvMetricsEndpoint, "")
	if MetricsEndpoint() != "http://ignored:4318/v1/metrics" {
		t.Fatal("base endpoint + /v1/metrics")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
	defer srv.Close()
	t.Setenv(EnvEndpoint, srv.URL)
	t.Setenv(EnvExportInterval, "10") // below the floor → default
	exp := NewFromEnv(func() []Metric { return nil }, nil)
	if exp.interval != defaultInterval {
		t.Fatal("interval floor")
	}
	if err := exp.Push(context.Background()); err == nil {
		t.Fatal("HTTP 500 must be an error")
	}
	if _, _, lastErr := exp.Status(); lastErr == "" {
		t.Fatal("status must carry the last error")
	}
	var nilExp *Exporter
	nilExp.Start(context.Background())
	nilExp.Stop(context.Background())
	if err := nilExp.Push(context.Background()); err != nil {
		t.Fatal("nil exporter is a no-op")
	}
	if h := parseHeaders("a=1, b=x%2Fy ,bad"); h["a"] != "1" || h["b"] != "x/y" || len(h) != 2 {
		t.Fatalf("headers = %v", h)
	}
}
