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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/diillson/chatcli/operator/api/v1alpha1"
)

// The four policy kinds are readable over the API, per namespace or
// across all, with their raw spec and status; writes are refused.
func TestPolicies_ReadOnlyListAndGet(t *testing.T) {
	objs := []*v1alpha1.ApprovalPolicy{
		{ObjectMeta: metav1.ObjectMeta{Name: "prod-rollbacks", Namespace: "production"}, Spec: v1alpha1.ApprovalPolicySpec{Enabled: true, Rules: []v1alpha1.ApprovalRule{{Name: "rollback", Mode: v1alpha1.ApprovalModeManual}}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "staging-auto", Namespace: "staging"}, Spec: v1alpha1.ApprovalPolicySpec{Enabled: true}},
	}
	sla := &v1alpha1.IncidentSLA{ObjectMeta: metav1.ObjectMeta{Name: "gold", Namespace: "production"}, Spec: v1alpha1.IncidentSLASpec{Severity: v1alpha1.IssueSeverityCritical, ResponseTime: "15m", ResolutionTime: "1h"}}
	c := fake.NewClientBuilder().WithScheme(newRestScheme()).WithObjects(objs[0], objs[1], sla).Build()
	api := NewAPIServer(c, ":0")

	do := func(method, path string) (int, map[string]any) {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, path, nil)
		r = r.WithContext(context.WithValue(r.Context(), contextKeyRole, "viewer"))
		api.routeAPI(w, r)
		var body map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return w.Code, body
	}

	code, body := do(http.MethodGet, "/api/v1/policies/approval")
	if code != http.StatusOK || body["kind"] != "ApprovalPolicyList" {
		t.Fatalf("list approval = %d %v", code, body)
	}
	if n := body["metadata"].(map[string]any)["totalCount"].(float64); n != 2 {
		t.Fatalf("approval policies across namespaces = %v, want 2", n)
	}
	code, body = do(http.MethodGet, "/api/v1/policies/approval?namespace=production")
	items := body["items"].([]any)
	if code != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["name"] != "prod-rollbacks" {
		t.Fatalf("production approval policies = %d %v", code, body)
	}
	if rules := items[0].(map[string]any)["spec"].(map[string]any)["rules"]; rules == nil {
		t.Fatal("spec must be served raw, rules missing")
	}

	code, body = do(http.MethodGet, "/api/v1/policies/sla/gold?namespace=production")
	if code != http.StatusOK || body["kind"] != "IncidentSLA" || body["spec"].(map[string]any)["resolutionTime"] != "1h" {
		t.Fatalf("get sla = %d %v", code, body)
	}

	if code, _ := do(http.MethodGet, "/api/v1/policies/budget"); code != http.StatusNotFound {
		t.Fatalf("unknown kind = %d, want 404", code)
	}
	if code, _ := do(http.MethodPost, "/api/v1/policies/approval"); code != http.StatusMethodNotAllowed {
		t.Fatalf("POST = %d, want 405", code)
	}
}
