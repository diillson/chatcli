/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
	"github.com/diillson/chatcli/operator/controllers"
)

func sloWithStatus(name string, met bool, remaining float64, alerts int) *v1alpha1.ServiceLevelObjective {
	now := metav1.Now()
	slo := &v1alpha1.ServiceLevelObjective{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop"},
		Spec: v1alpha1.ServiceLevelObjectiveSpec{ServiceName: name, Enabled: true,
			Indicator: v1alpha1.SLOIndicator{Type: v1alpha1.SLOIndicatorAvailability, MetricSource: v1alpha1.SLOSourceIssues},
			Target:    v1alpha1.SLOTarget{Percentage: 99.9, Window: "30d"}},
		Status: v1alpha1.ServiceLevelObjectiveStatus{
			CurrentValue: 0.9995, TargetMet: met, ErrorBudgetTotal: 0.001, ErrorBudgetRemaining: remaining,
			ErrorBudgetConsumedPercentage: (1 - remaining) * 100, BurnRate1h: 2.5, BurnRate6h: 1.5, BurnRate24h: 0.8, BurnRate72h: 0.4,
			LastCalculatedAt: &now,
		},
	}
	for i := 0; i < alerts; i++ {
		slo.Status.ActiveAlerts = append(slo.Status.ActiveAlerts, v1alpha1.SLOAlert{Window: "1h/6h", Severity: v1alpha1.IssueSeverityHigh, FiredAt: now})
	}
	return slo
}

func getJSON(t *testing.T, api *APIServer, path string) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r = r.WithContext(context.WithValue(r.Context(), contextKeyRole, "viewer"))
	api.routeAPI(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", path, w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// The SLO view reads the fields the SLO controller writes: burn rates, the
// used budget and a state derived from targetMet, budget and alerts; the
// summary counts at-risk SLOs from the same derivation.
func TestSLOs_ReadRealStatusFields(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newRestScheme()).WithObjects(
		sloWithStatus("healthy", true, 0.8, 0),
		sloWithStatus("burning", true, 0.4, 1),
		sloWithStatus("breached", false, 0, 0),
	).Build()
	api := NewAPIServer(c, ":0")

	byName := map[string]map[string]any{}
	for _, it := range getJSON(t, api, "/api/v1/slos")["items"].([]any) {
		m := it.(map[string]any)
		byName[m["name"].(string)] = m
	}
	for name, want := range map[string]string{"healthy": "Healthy", "burning": "AtRisk", "breached": "Breached"} {
		if got := byName[name]["state"]; got != want {
			t.Fatalf("%s state = %v, want %s", name, got, want)
		}
	}
	h := byName["healthy"]
	if h["burnRate1h"].(float64) != 2.5 || h["burnRate72h"].(float64) != 0.4 {
		t.Fatalf("burn rates not served: %v", h)
	}
	if used := h["errorBudgetUsed"].(float64); used < 0.00019 || used > 0.00021 {
		t.Fatalf("errorBudgetUsed = %v, want 0.0002 (20%% of 0.001)", used)
	}
	if byName["breached"]["errorBudgetRemaining"] != float64(0) {
		t.Fatalf("an exhausted budget must be served as 0, got %v", byName["breached"]["errorBudgetRemaining"])
	}

	summary := getJSON(t, api, "/api/v1/analytics/summary")["spec"].(map[string]any)
	if summary["slosAtRisk"].(float64) != 2 {
		t.Fatalf("slosAtRisk = %v, want 2 (AtRisk + Breached)", summary["slosAtRisk"])
	}

	budget := getJSON(t, api, "/api/v1/slos/healthy/budget?namespace=shop")["spec"].(map[string]any)
	if budget["burnRate"].(float64) != 2.5 || budget["state"] != "Healthy" {
		t.Fatalf("budget = %v, want the 1h burn rate and the derived state", budget)
	}
}

func TestSLOState_NotCalculatedYetIsEmpty(t *testing.T) {
	if got := sloStateFromStatus(map[string]interface{}{"targetMet": true}); got != "" {
		t.Fatalf("state before the first calculation = %q, want empty", got)
	}
}

// The correlations endpoint reads the annotations the federation
// controller writes, and still the legacy ones.
func TestFederationCorrelations_ReadControllerAnnotations(t *testing.T) {
	mk := func(name string, ann map[string]string) *v1alpha1.Issue {
		return &v1alpha1.Issue{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Annotations: ann},
			Spec: v1alpha1.IssueSpec{Severity: v1alpha1.IssueSeverityCritical, SignalType: "oom_kill"}}
	}
	c := fake.NewClientBuilder().WithScheme(newRestScheme()).WithObjects(
		mk("current", map[string]string{
			controllers.AnnotationCrossClusterCorrelation: "xcluster-new1",
			controllers.AnnotationAffectedClusters:        "3",
			controllers.AnnotationSeverityElevated:        "true",
		}),
		mk("peer", map[string]string{controllers.AnnotationCrossClusterCorrelation: "xcluster-new1"}),
		mk("legacy", map[string]string{"platform.chatcli.io/correlation-id": "old-7", "platform.chatcli.io/correlated-clusters": "4"}),
		mk("plain", nil),
	).Build()
	api := NewAPIServer(c, ":0")
	items := getJSON(t, api, "/api/v1/federation/correlations")["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("correlations = %v, want one per correlation ID", items)
	}
	got := map[string]map[string]any{}
	for _, it := range items {
		m := it.(map[string]any)
		got[m["correlationId"].(string)] = m
	}
	if cur := got["xcluster-new1"]; cur == nil || cur["correlatedClusters"] != "3" || cur["elevated"] != true {
		t.Fatalf("canonical correlation = %v", cur)
	}
	if old := got["old-7"]; old == nil || old["correlatedClusters"] != "4" {
		t.Fatalf("legacy correlation = %v", old)
	}
}

// global-status counts degraded clusters (reachable, nodes not Ready).
func TestGlobalClusterStatus_CountsDegraded(t *testing.T) {
	mk := func(name string, connected, degraded bool) *v1alpha1.ClusterRegistration {
		cr := &v1alpha1.ClusterRegistration{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "fed"},
			Status: v1alpha1.ClusterRegistrationStatus{Connected: connected}}
		if degraded {
			cr.Status.Conditions = []metav1.Condition{{Type: controllers.ClusterConditionDegraded, Status: metav1.ConditionTrue, Reason: "NodesNotReady", LastTransitionTime: metav1.Now()}}
		}
		return cr
	}
	c := fake.NewClientBuilder().WithScheme(newRestScheme()).WithObjects(
		mk("ok", true, false), mk("sick", true, true), mk("gone", false, false)).Build()
	api := NewAPIServer(c, ":0")
	spec := getJSON(t, api, "/api/v1/clusters/global-status")["spec"].(map[string]any)
	if spec["healthyClusters"].(float64) != 1 || spec["degradedClusters"].(float64) != 1 || spec["offlineClusters"].(float64) != 1 {
		t.Fatalf("global status = %v, want 1 healthy, 1 degraded, 1 offline", spec)
	}
	fed := getJSON(t, api, "/api/v1/federation/status")["spec"].(map[string]any)
	if fed["degradedClusters"].(float64) != 1 || fed["connectedClusters"].(float64) != 2 {
		t.Fatalf("federation status = %v", fed)
	}
}

// Acknowledge and snooze write the annotations the notification controller
// acts on; a snooze needs a positive duration.
func TestAckAndSnooze_WriteControllerAnnotations(t *testing.T) {
	iss := &v1alpha1.Issue{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "default"}}
	c := fake.NewClientBuilder().WithScheme(newRestScheme()).WithObjects(iss).Build()
	api := NewAPIServer(c, ":0")
	post := func(path, body string) int {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r = r.WithContext(context.WithValue(r.Context(), contextKeyRole, "operator"))
		api.routeAPI(w, r)
		return w.Code
	}
	if code := post("/api/v1/incidents/db/snooze?namespace=default", `{"duration":"-5m"}`); code != http.StatusBadRequest {
		t.Fatalf("negative snooze = %d, want 400", code)
	}
	if code := post("/api/v1/incidents/db/snooze?namespace=default", `{"duration":"30m"}`); code != http.StatusOK {
		t.Fatalf("snooze = %d", code)
	}
	if code := post("/api/v1/incidents/db/acknowledge?namespace=default", ``); code != http.StatusOK {
		t.Fatalf("acknowledge = %d", code)
	}
	var got v1alpha1.Issue
	if err := c.Get(context.Background(), types.NamespacedName{Name: "db", Namespace: "default"}, &got); err != nil {
		t.Fatal(err)
	}
	if !controllers.IncidentAcknowledged(&got) {
		t.Fatalf("acknowledge did not set %s: %v", controllers.AnnotationIncidentAcknowledged, got.Annotations)
	}
	until, snoozed := controllers.IncidentSnoozedUntil(&got, time.Now())
	if !snoozed || time.Until(until) < 29*time.Minute {
		t.Fatalf("snooze not readable by the controller: %v", got.Annotations)
	}
}
