/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package rest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// The cost endpoint serves the ledger the controllers write, per namespace
// or across all of them, honoring the time range like the other analytics.
func TestAnalyticsCost_ServesTheLedger(t *testing.T) {
	entry := func(name string, usd float64, at time.Time) string {
		b, _ := json.Marshal(map[string]any{
			"issueName": name, "llmCosts": map[string]any{"estimatedCostUSD": usd}, "totalCostUSD": usd, "recordedAt": at,
		})
		return string(b)
	}
	labels := map[string]string{"app.kubernetes.io/managed-by": "chatcli-operator"}
	objs := []*corev1.ConfigMap{
		{ObjectMeta: metav1.ObjectMeta{Name: "chatcli-cost-ledger", Namespace: "production", Labels: labels},
			Data: map[string]string{"a": entry("a", 0.5, time.Now()), "b": entry("b", 0.25, time.Now().Add(-72*time.Hour))}},
		{ObjectMeta: metav1.ObjectMeta{Name: "chatcli-cost-ledger", Namespace: "staging", Labels: labels},
			Data: map[string]string{"c": entry("c", 1, time.Now())}},
	}
	c := fake.NewClientBuilder().WithScheme(newRestScheme()).WithObjects(objs[0], objs[1]).Build()
	api := NewAPIServer(c, ":0")

	call := func(query string, tr timeRangeParams) map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/analytics/cost"+query, nil)
		api.handleAnalyticsCost(w, r, tr)
		if w.Code != http.StatusOK {
			t.Fatalf("GET cost%s = %d: %s", query, w.Code, w.Body.String())
		}
		var resp struct {
			Kind string         `json:"kind"`
			Spec map[string]any `json:"spec"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.Kind != "CostSummary" {
			t.Fatalf("kind = %q", resp.Kind)
		}
		return resp.Spec
	}

	all := call("", timeRangeParams{})
	if all["incidentCount"].(float64) != 3 || all["totalLLMCost"].(float64) != 1.75 {
		t.Fatalf("all namespaces, 30 days = %v", all)
	}
	prod := call("?namespace=production", timeRangeParams{})
	if prod["incidentCount"].(float64) != 2 || prod["totalLLMCost"].(float64) != 0.75 {
		t.Fatalf("production = %v", prod)
	}
	from, to := time.Now().Add(-24*time.Hour), time.Now()
	recent := call("?namespace=production", timeRangeParams{From: &from, To: &to})
	if recent["incidentCount"].(float64) != 1 || recent["totalLLMCost"].(float64) != 0.5 {
		t.Fatalf("production, last day = %v", recent)
	}
}
