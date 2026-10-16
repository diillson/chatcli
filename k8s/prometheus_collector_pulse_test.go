/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package k8s

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/pkg/pulse/pulsetest"
	"go.uber.org/zap"
)

// Scrapes of a workload's /metrics endpoint show as connections on the live
// dashboard, from either constructor.
func TestPrometheusCollectorScrapesAreMetered(t *testing.T) {
	rec := pulsetest.Record(t)
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	for _, c := range []*PrometheusCollector{
		NewPrometheusCollector(nil, "ns", "app", 9090, "", nil, zap.NewNop()),
		NewResourcePrometheusCollector(nil, "ns", "app", "StatefulSet", 9090, "", nil, zap.NewNop()),
	} {
		resp, err := c.httpClient.Get(srv.URL + "/metrics")
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	rec.WaitFor(t, "two metered scrapes", func(pulse.Event) bool {
		return len(rec.Match(pulsetest.Is(pulse.KindConn, "127.0.0.1", pulse.PhaseEnd))) >= 2
	})
}
