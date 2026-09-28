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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

func approvalAPI(t *testing.T, state v1alpha1.ApprovalRequestState, funcs *interceptor.Funcs) (*APIServer, client.Client, http.Handler) {
	t.Helper()
	ar := &v1alpha1.ApprovalRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "approval-plan-1", Namespace: "default"},
		Spec:       v1alpha1.ApprovalRequestSpec{PolicyRef: "p", RuleName: "r", RequiredApprovers: 2, TimeoutMinutes: 30},
		Status:     v1alpha1.ApprovalRequestStatus{State: state},
	}
	b := fake.NewClientBuilder().WithScheme(newRestScheme()).WithStatusSubresource(&v1alpha1.ApprovalRequest{}).WithObjects(ar)
	if funcs != nil {
		b = b.WithInterceptorFuncs(*funcs)
	}
	c := b.Build()
	s := NewAPIServer(c, ":0")
	s.SetAPIKeyEntries(map[string]APIKey{
		"key-1": {Role: "operator", Name: "alice"},
		"key-2": {Role: "operator"},
		"key-3": {Role: "viewer", Name: "carol"},
	})
	return s, c, s.authMiddleware(http.HandlerFunc(s.routeAPI))
}

func postDecision(h http.Handler, key, verb, approver string) *httptest.ResponseRecorder {
	body := `{"approver":"` + approver + `","reason":"checked"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/approvals/approval-plan-1/"+verb+"?namespace=default", strings.NewReader(body))
	req.Header.Set("X-API-Key", key)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func storedApproval(t *testing.T, c client.Client) v1alpha1.ApprovalRequest {
	t.Helper()
	var ar v1alpha1.ApprovalRequest
	if err := c.Get(context.Background(), types.NamespacedName{Name: "approval-plan-1", Namespace: "default"}, &ar); err != nil {
		t.Fatal(err)
	}
	return ar
}

// Item 2: the REST API records a decision (schema fields, approver with
// the key identity) and leaves the state to the ApprovalReconciler.
func TestApprovalREST_RecordsDecisionOnly(t *testing.T) {
	_, c, h := approvalAPI(t, v1alpha1.ApprovalStatePending, nil)
	rec := postDecision(h, "key-1", "approve", "Alice Doe")
	if rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	ar := storedApproval(t, c)
	if ar.Status.State != v1alpha1.ApprovalStatePending {
		t.Fatalf("REST set the state itself: %s", ar.Status.State)
	}
	if len(ar.Status.Decisions) != 1 || ar.Status.Decisions[0].Approver != "Alice Doe (api-key: alice)" ||
		ar.Status.Decisions[0].Decision != "approved" || ar.Status.Decisions[0].Timestamp.IsZero() {
		t.Fatalf("decisions = %+v", ar.Status.Decisions)
	}
	var resp struct {
		Spec ApprovalItem `json:"spec"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Spec.RequiredApprovers != 2 || len(resp.Spec.Decisions) != 1 || resp.Spec.ApprovedBy != "Alice Doe (api-key: alice)" {
		t.Fatalf("response item = %+v", resp.Spec)
	}

	// The same key cannot approve twice under another name.
	if rec := postDecision(h, "key-1", "approve", "Someone Else"); rec.Code != http.StatusConflict {
		t.Fatalf("second decision by the same key: %d", rec.Code)
	}
	// A key without a name is identified by its fingerprint.
	if rec := postDecision(h, "key-2", "approve", "bob"); rec.Code != http.StatusOK {
		t.Fatalf("approve by key-2: %d %s", rec.Code, rec.Body.String())
	}
	ar = storedApproval(t, c)
	if got := ar.Status.Decisions[1].Approver; !strings.HasPrefix(got, "bob (api-key: key-") || strings.Contains(got, "key-2") {
		t.Fatalf("unnamed key approver = %q", got)
	}
	// Viewers cannot decide; an empty name is refused.
	if rec := postDecision(h, "key-3", "approve", "carol"); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer decision: %d", rec.Code)
	}
	if rec := postDecision(h, "key-1", "reject", "  "); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty approver: %d", rec.Code)
	}
}

// Item 2: only Pending requests take decisions; Rejected→Approved and
// Expired→Approved flips are refused with 409.
func TestApprovalREST_FinishedRequestConflicts(t *testing.T) {
	for _, state := range []v1alpha1.ApprovalRequestState{v1alpha1.ApprovalStateRejected, v1alpha1.ApprovalStateExpired, v1alpha1.ApprovalStateApproved} {
		_, c, h := approvalAPI(t, state, nil)
		if rec := postDecision(h, "key-1", "approve", "alice"); rec.Code != http.StatusConflict {
			t.Fatalf("%s: approve = %d, want 409", state, rec.Code)
		}
		if ar := storedApproval(t, c); ar.Status.State != state || len(ar.Status.Decisions) != 0 {
			t.Fatalf("%s: request changed: %+v", state, ar.Status)
		}
	}
}

// Item 2: a failed status write is an error, never a fallback to a plain
// object update.
func TestApprovalREST_NoPlainUpdateFallback(t *testing.T) {
	updates := 0
	funcs := &interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			return apierrors.NewForbidden(v1alpha1.GroupVersion.WithResource("approvalrequests").GroupResource(), obj.GetName(), nil)
		},
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			updates++
			return c.Update(ctx, obj, opts...)
		},
	}
	_, _, h := approvalAPI(t, v1alpha1.ApprovalStatePending, funcs)
	if rec := postDecision(h, "key-1", "approve", "alice"); rec.Code != http.StatusInternalServerError {
		t.Fatalf("approve with a failing status write = %d", rec.Code)
	}
	if updates != 0 {
		t.Fatalf("plain Update called %d times", updates)
	}
}

// Item 2: removing every key fails closed (item 3 relies on it).
func TestAPIKeyEntries_EmptyRejects(t *testing.T) {
	s, _, h := approvalAPI(t, v1alpha1.ApprovalStatePending, nil)
	s.SetAPIKeyEntries(map[string]APIKey{})
	t.Setenv("CHATCLI_OPERATOR_DEV_MODE", "")
	if rec := postDecision(h, "key-1", "approve", "alice"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked key: %d, want 401", rec.Code)
	}
}
