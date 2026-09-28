/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package rest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

func callAPI(t *testing.T, api *APIServer, method, path, body, role string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r = r.WithContext(context.WithValue(r.Context(), contextKeyRole, role))
	api.routeAPI(w, r)
	return w
}

// Only an existing runbook is a conflict; other create failures keep their
// own status.
func TestCreateRunbook_ErrorStatusCodes(t *testing.T) {
	existing := &v1alpha1.Runbook{ObjectMeta: metav1.ObjectMeta{Name: "scale", Namespace: "default"}}
	api := NewAPIServer(fake.NewClientBuilder().WithScheme(newRestScheme()).WithObjects(existing).Build(), ":0")
	if w := callAPI(t, api, http.MethodPost, "/api/v1/runbooks", `{"name":"scale","namespace":"default"}`, "operator"); w.Code != http.StatusConflict {
		t.Fatalf("duplicate runbook = %d, want 409", w.Code)
	}

	failing := func(err error) *APIServer {
		c := fake.NewClientBuilder().WithScheme(newRestScheme()).WithInterceptorFuncs(interceptor.Funcs{
			Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error { return err },
		}).Build()
		return NewAPIServer(c, ":0")
	}
	gk := schema.GroupKind{Group: "platform.chatcli.io", Kind: "Runbook"}
	cases := map[int]error{
		http.StatusUnprocessableEntity: apierrors.NewInvalid(gk, "bad", nil),
		http.StatusForbidden:           apierrors.NewForbidden(schema.GroupResource{Resource: "runbooks"}, "x", errors.New("rbac")),
		http.StatusInternalServerError: errors.New("etcd unavailable"),
	}
	for want, err := range cases {
		if w := callAPI(t, failing(err), http.MethodPost, "/api/v1/runbooks", `{"name":"x"}`, "operator"); w.Code != want {
			t.Fatalf("create failing with %v = %d, want %d", err, w.Code, want)
		}
	}
}

// The audit trail is served newest first.
func TestListAuditEvents_NewestFirst(t *testing.T) {
	mk := func(name string, at time.Time) *v1alpha1.AuditEvent {
		return &v1alpha1.AuditEvent{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: v1alpha1.AuditEventSpec{EventType: "issue_created", Timestamp: metav1.NewTime(at)}}
	}
	now := time.Now().UTC().Truncate(time.Second)
	c := fake.NewClientBuilder().WithScheme(newRestScheme()).WithObjects(
		mk("b-middle", now.Add(-time.Hour)), mk("a-oldest", now.Add(-2*time.Hour)), mk("c-newest", now)).Build()
	w := callAPI(t, NewAPIServer(c, ":0"), http.MethodGet, "/api/v1/audit", "", "viewer")
	var resp struct {
		Items []AuditEventItem `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, it := range resp.Items {
		names = append(names, it.Name)
	}
	if strings.Join(names, ",") != "c-newest,b-middle,a-oldest" {
		t.Fatalf("order = %v, want newest first", names)
	}
}

// Approvals carry their annotations, where the blast-radius badge lives.
func TestApprovals_CarryAnnotations(t *testing.T) {
	ar := &v1alpha1.ApprovalRequest{ObjectMeta: metav1.ObjectMeta{Name: "ar", Namespace: "default",
		Annotations: map[string]string{"platform.chatcli.io/blast-risk-level": "high"}}}
	c := fake.NewClientBuilder().WithScheme(newRestScheme()).WithObjects(ar).Build()
	w := callAPI(t, NewAPIServer(c, ":0"), http.MethodGet, "/api/v1/approvals", "", "viewer")
	if !strings.Contains(w.Body.String(), `"platform.chatcli.io/blast-risk-level":"high"`) {
		t.Fatalf("approval list lacks the blast-radius annotation: %s", w.Body.String())
	}
}

// MTTD and MTTR leave chaos-induced Issues out, as the dashboard says.
func TestMTTR_ExcludesChaosIssues(t *testing.T) {
	created := metav1.NewTime(time.Now().Add(-2 * time.Hour))
	resolved := metav1.NewTime(time.Now().Add(-time.Hour))
	mk := func(name string, chaos bool) *v1alpha1.Issue {
		iss := &v1alpha1.Issue{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", CreationTimestamp: created},
			Status: v1alpha1.IssueStatus{State: v1alpha1.IssueStateResolved, ResolvedAt: &resolved, DetectedAt: &created}}
		if chaos {
			iss.Labels = map[string]string{"platform.chatcli.io/source": "chaos-experiment"}
		}
		return iss
	}
	c := fake.NewClientBuilder().WithScheme(newRestScheme()).WithObjects(mk("real", false), mk("drill", true)).Build()
	api := NewAPIServer(c, ":0")
	for _, compute := range []func(context.Context, timeRangeParams) ([]MTTMetric, error){api.computeMTTR, api.computeMTTD} {
		metrics, err := compute(context.Background(), timeRangeParams{})
		if err != nil {
			t.Fatal(err)
		}
		total := 0
		for _, m := range metrics {
			total += m.Count
		}
		if total != 1 {
			t.Fatalf("issues counted = %d, want only the non-chaos one", total)
		}
	}
}
