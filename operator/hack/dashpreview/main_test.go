/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	v1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// The seed has to cover every tab of the dashboard, otherwise the preview
// shows empty panels and a visual review misses them.
func TestSeedCoversEveryDashboardTab(t *testing.T) {
	kinds := map[string]int{}
	for _, o := range seed() {
		kinds[fmt.Sprintf("%T", o)]++
	}
	for _, want := range []string{
		"*v1alpha1.Issue", "*v1alpha1.AIInsight", "*v1alpha1.RemediationPlan", "*v1alpha1.ApprovalRequest",
		"*v1alpha1.PostMortem", "*v1alpha1.ServiceLevelObjective", "*v1alpha1.ClusterRegistration", "*v1alpha1.AuditEvent", "*v1alpha1.Runbook",
	} {
		if kinds[want] == 0 {
			t.Errorf("seed has no %s; the matching dashboard tab would be empty", want)
		}
	}
	if kinds["*v1alpha1.Issue"] < 5 {
		t.Errorf("seed has %d issues, want at least 5 so every state is represented", kinds["*v1alpha1.Issue"])
	}
	for _, o := range seed() {
		if rb, ok := o.(*v1.Runbook); ok && len(rb.Spec.Steps) == 0 {
			t.Errorf("runbook %s has no steps; the expanded row would show nothing", rb.Name)
		}
	}
}

func TestListenAddrDefaultsAndOverride(t *testing.T) {
	t.Setenv("DASHPREVIEW_ADDR", "")
	if got := listenAddr(); got != defaultAddr {
		t.Fatalf("listenAddr() = %q, want %q", got, defaultAddr)
	}
	t.Setenv("DASHPREVIEW_ADDR", "127.0.0.1:1")
	if got := listenAddr(); got != "127.0.0.1:1" {
		t.Fatalf("listenAddr() = %q, want the override", got)
	}
}

// The preview serves the real embedded dashboard and the seeded API behind
// the preview key, and stops when the context is canceled.
func TestRunServesDashboardAndSeededAPI(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, addr) }()

	client := &http.Client{Timeout: 2 * time.Second}
	base := "http://" + addr
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := client.Get(base + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("preview did not come up on %s: %v", addr, err)
		}
		time.Sleep(50 * time.Millisecond)
	}

	page, err := client.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = page.Body.Close()
	if page.StatusCode != http.StatusOK {
		t.Fatalf("GET / = %d, want 200 (embedded dashboard)", page.StatusCode)
	}

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/runbooks?pageSize=10", nil)
	req.Header.Set("X-API-Key", previewAPIKey)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/runbooks = %d, want 200 with the preview key", resp.StatusCode)
	}
	var body struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) < 2 {
		t.Fatalf("runbooks served = %d, want the seeded runbooks", len(body.Items))
	}

	anon, err := client.Get(base + "/api/v1/runbooks")
	if err != nil {
		t.Fatal(err)
	}
	_ = anon.Body.Close()
	if anon.StatusCode == http.StatusOK {
		t.Fatalf("API answered without a key; the preview must keep the real auth middleware")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v after cancel", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("run did not stop after the context was canceled")
	}
}
