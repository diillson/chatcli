/*
 * ChatCLI - Kubernetes Operator
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package rest

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// TestAckHumanActionEndpoint_StatusUpdateConflictReturns409 covers the
// dashboard-UX guard added to handlePostMortemAckHumanAction: when the
// Status().Update call to clear RequiresHumanAction races with another
// writer (controller reconciler, parallel API call), the handler returns
// HTTP 409 instead of swallowing the error or returning a misleading 500.
// The dashboard's retry-on-409 logic then kicks in cleanly.
//
// Without explicit coverage of this branch, a regression that swapped the
// conflict check for a generic error path would only surface as silent
// failures in production — the patch-coverage gate caught the gap on the
// first run of PR #957.
func TestAckHumanActionEndpoint_StatusUpdateConflictReturns409(t *testing.T) {
	s := newRestScheme()
	pm := &v1alpha1.PostMortem{
		ObjectMeta: metav1.ObjectMeta{Name: "pm-conflict", Namespace: "default"},
		Spec: v1alpha1.PostMortemSpec{
			IssueRef: v1alpha1.IssueRef{Name: "iss"}, Resource: v1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: "default"}, Severity: v1alpha1.IssueSeverityHigh,
		},
		Status: v1alpha1.PostMortemStatus{
			State:               v1alpha1.PostMortemStateOpen,
			RequiresHumanAction: true,
			RequiredAction:      "restore replicas",
		},
	}

	// Interceptor returns a Conflict error on the Status().Update path only.
	// The Spec Update (annotation persistence) still goes through, then the
	// follow-up Status update races.
	conflict := apierrors.NewConflict(
		schema.GroupResource{Group: "platform.chatcli.io", Resource: "postmortems"},
		pm.Name,
		fmt.Errorf("the object has been modified; please apply your changes to the latest version and try again"),
	)
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&v1alpha1.PostMortem{}).
		WithObjects(pm).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, client client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				if subResourceName == "status" {
					return conflict
				}
				return client.Status().Update(ctx, obj, opts...)
			},
		}).
		Build()
	api := NewAPIServer(c, ":0")

	body := strings.NewReader(`{"acknowledgedBy":"sre"}`)
	req := httptest.NewRequest("POST", "/api/v1/postmortems/pm-conflict/ack-human-action?namespace=default", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	api.handlePostMortemAckHumanAction(w, req, "pm-conflict")

	if w.Code != 409 {
		t.Fatalf("status: want 409 Conflict on Status().Update conflict, got %d (body=%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "modified concurrently") {
		t.Fatalf("response body should mention concurrent modification so the dashboard knows to retry, got %q", w.Body.String())
	}
}

// TestAckHumanActionEndpoint_StatusUpdateGenericErrorReturns500 covers the
// non-conflict error branch: anything other than IsConflict (network blip,
// permission denied, malformed payload from the apiserver) surfaces as a
// 500 with the original error message so the operator can diagnose without
// digging through pod logs.
func TestAckHumanActionEndpoint_StatusUpdateGenericErrorReturns500(t *testing.T) {
	s := newRestScheme()
	pm := &v1alpha1.PostMortem{
		ObjectMeta: metav1.ObjectMeta{Name: "pm-status-broken", Namespace: "default"},
		Spec: v1alpha1.PostMortemSpec{
			IssueRef: v1alpha1.IssueRef{Name: "iss"}, Resource: v1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: "default"}, Severity: v1alpha1.IssueSeverityHigh,
		},
		Status: v1alpha1.PostMortemStatus{
			State:               v1alpha1.PostMortemStateOpen,
			RequiresHumanAction: true,
			RequiredAction:      "restore replicas",
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&v1alpha1.PostMortem{}).
		WithObjects(pm).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, client client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				if subResourceName == "status" {
					return fmt.Errorf("apiserver unavailable: broken-pipe")
				}
				return client.Status().Update(ctx, obj, opts...)
			},
		}).
		Build()
	api := NewAPIServer(c, ":0")

	req := httptest.NewRequest("POST", "/api/v1/postmortems/pm-status-broken/ack-human-action?namespace=default", strings.NewReader(""))
	w := httptest.NewRecorder()
	api.handlePostMortemAckHumanAction(w, req, "pm-status-broken")

	if w.Code != 500 {
		t.Fatalf("status: want 500 on non-conflict Status().Update error, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "apiserver unavailable") {
		t.Fatalf("response body should surface the underlying error so operators can diagnose, got %q", w.Body.String())
	}
}
