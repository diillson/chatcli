/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"context"
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// A write that loses an optimistic concurrency race, as on every rollout,
// is retried shortly instead of surfacing as a reconciler error; any other
// write failure is still an error.
func TestReconcile_ConflictIsRetriedNotReported(t *testing.T) {
	conflict := errors.NewConflict(schema.GroupResource{Group: "platform.chatcli.io", Resource: "instances"}, "web", fmt.Errorf("the object has been modified"))
	cases := map[string]struct {
		updateErr error
		wantErr   bool
	}{
		"conflict":          {updateErr: fmt.Errorf("wrapped: %w", conflict)},
		"other write error": {updateErr: errors.NewForbidden(schema.GroupResource{Resource: "instances"}, "web", fmt.Errorf("denied")), wantErr: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			inst := newInstance("web", "default")
			c := fake.NewClientBuilder().WithScheme(newScheme()).
				WithStatusSubresource(&platformv1alpha1.Instance{}).
				WithObjects(inst).
				WithInterceptorFuncs(interceptor.Funcs{
					Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
						return tc.updateErr
					},
				}).Build()
			r := &InstanceReconciler{Client: c, Scheme: newScheme()}

			res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "web", Namespace: "default"}})
			if tc.wantErr {
				if err == nil {
					t.Fatal("a non-conflict write error must be returned")
				}
				return
			}
			if err != nil {
				t.Fatalf("conflict surfaced as error: %v", err)
			}
			if res.RequeueAfter != conflictRetryInterval {
				t.Fatalf("RequeueAfter = %v, want %v", res.RequeueAfter, conflictRetryInterval)
			}
		})
	}
}
