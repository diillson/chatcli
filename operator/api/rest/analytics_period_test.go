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
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

func getPeriodJSON(t *testing.T, api *APIServer, path string) map[string]any {
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

func TestAnalyticsPeriod(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 1, 8, 0, 0, 0, 0, time.UTC)
	if s, e := analyticsPeriod(timeRangeParams{From: &from, To: &to}, time.Hour); !s.Equal(from) || !e.Equal(to) {
		t.Fatalf("from+to = [%v, %v], want exactly the query", s, e)
	}
	if s, e := analyticsPeriod(timeRangeParams{To: &to}, 24*time.Hour); !e.Equal(to) || !s.Equal(to.Add(-24*time.Hour)) {
		t.Fatalf("to only = [%v, %v], want the default window ending at to", s, e)
	}
	if s, e := analyticsPeriod(timeRangeParams{}, 24*time.Hour); time.Since(e) > time.Minute || e.Sub(s) != 24*time.Hour {
		t.Fatalf("defaults = [%v, %v], want the last 24h", s, e)
	}
}

// The compliance report covers the requested period, not a window ending now.
func TestAnalyticsCompliance_HonorsAbsolutePeriod(t *testing.T) {
	old := &v1alpha1.Issue{ObjectMeta: metav1.ObjectMeta{Name: "old", Namespace: "default", CreationTimestamp: metav1.NewTime(time.Now().Add(-10 * 24 * time.Hour))}}
	fresh := &v1alpha1.Issue{ObjectMeta: metav1.ObjectMeta{Name: "fresh", Namespace: "default", CreationTimestamp: metav1.Now()}}
	c := fake.NewClientBuilder().WithScheme(newRestScheme()).WithObjects(old, fresh).Build()
	api := NewAPIServer(c, ":0")
	from := time.Now().Add(-12 * 24 * time.Hour).UTC().Truncate(time.Second)
	to := time.Now().Add(-8 * 24 * time.Hour).UTC().Truncate(time.Second)
	spec := getPeriodJSON(t, api, "/api/v1/analytics/compliance?from="+from.Format(time.RFC3339)+"&to="+to.Format(time.RFC3339))["spec"].(map[string]any)
	incidents := spec["IncidentMetrics"].(map[string]any)
	if incidents["TotalIncidents"].(float64) != 1 {
		t.Fatalf("TotalIncidents = %v, want only the Issue inside [from, to]", incidents["TotalIncidents"])
	}
	if end := spec["Period"].(map[string]any)["End"].(string); !strings.HasPrefix(end, to.Format("2006-01-02T15:04:05")) {
		t.Fatalf("Period.End = %s, want %s", end, to.Format(time.RFC3339))
	}
}
