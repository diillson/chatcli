/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/go-logr/logr/funcr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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

// Every reconcile step that writes reports a non-conflict failure as an
// error, and a conflict as a quiet retry, whichever object it writes.
func TestReconcile_StepFailuresByObject(t *testing.T) {
	conflict := errors.NewConflict(schema.GroupResource{Resource: "objects"}, "web", fmt.Errorf("the object has been modified"))
	forbidden := errors.NewForbidden(schema.GroupResource{Resource: "objects"}, "web", fmt.Errorf("denied"))
	steps := map[string]func(client.Object) bool{
		"serviceaccount": func(o client.Object) bool { _, ok := o.(*corev1.ServiceAccount); return ok },
		"atrest key": func(o client.Object) bool {
			s, ok := o.(*corev1.Secret)
			return ok && strings.HasSuffix(s.Name, atrestKeySecretSuffix)
		},
		"configmap":  func(o client.Object) bool { _, ok := o.(*corev1.ConfigMap); return ok },
		"pvc":        func(o client.Object) bool { _, ok := o.(*corev1.PersistentVolumeClaim); return ok },
		"service":    func(o client.Object) bool { _, ok := o.(*corev1.Service); return ok },
		"deployment": func(o client.Object) bool { _, ok := o.(*appsv1.Deployment); return ok },
	}
	for step, match := range steps {
		for _, injected := range []error{conflict, forbidden} {
			t.Run(step+"/"+string(errors.ReasonForError(injected)), func(t *testing.T) {
				inst := newInstance("web", "default")
				inst.Finalizers = []string{finalizerName}
				inst.Spec.Persistence = &platformv1alpha1.PersistenceSpec{Enabled: true}
				fail := func(o client.Object) error {
					if match(o) {
						return injected
					}
					return nil
				}
				c := fake.NewClientBuilder().WithScheme(newScheme()).
					WithStatusSubresource(&platformv1alpha1.Instance{}).
					WithObjects(inst).
					WithInterceptorFuncs(interceptor.Funcs{
						Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
							if err := fail(obj); err != nil {
								return err
							}
							return c.Create(ctx, obj, opts...)
						},
						Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
							if err := fail(obj); err != nil {
								return err
							}
							return c.Update(ctx, obj, opts...)
						},
					}).Build()
				r := &InstanceReconciler{Client: c, Scheme: newScheme()}
				res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "web", Namespace: "default"}})
				if errors.IsConflict(injected) {
					if err != nil || res.RequeueAfter != conflictRetryInterval {
						t.Fatalf("conflict: res=%v err=%v, want a quiet retry", res, err)
					}
					return
				}
				if err == nil {
					t.Fatalf("a %s write failure was not reported", step)
				}
			})
		}
	}
}

// A status write that fails is reported; a conflicting one is retried.
func TestReconcile_StatusWriteFailure(t *testing.T) {
	inst := newInstance("web", "default")
	inst.Finalizers = []string{finalizerName}
	c := fake.NewClientBuilder().WithScheme(newScheme()).
		WithStatusSubresource(&platformv1alpha1.Instance{}).
		WithObjects(inst).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				return errors.NewForbidden(schema.GroupResource{Resource: "instances"}, "web", fmt.Errorf("denied"))
			},
			SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
				return errors.NewForbidden(schema.GroupResource{Resource: "instances"}, "web", fmt.Errorf("denied"))
			},
		}).Build()
	r := &InstanceReconciler{Client: c, Scheme: newScheme()}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "web", Namespace: "default"}}); err == nil {
		t.Fatal("a failed status write was not reported")
	}
}

// Conflicts are not logged as errors; other failures are.
func TestLogReconcileError(t *testing.T) {
	var lines []string
	logger := funcr.New(func(prefix, args string) { lines = append(lines, args) }, funcr.Options{})
	logReconcileError(logger, errors.NewConflict(schema.GroupResource{Resource: "deployments"}, "web", fmt.Errorf("modified")), "conflict")
	if len(lines) != 0 {
		t.Fatalf("a conflict was logged: %v", lines)
	}
	logReconcileError(logger, fmt.Errorf("boom"), "failed to reconcile Deployment")
	if len(lines) != 1 || !strings.Contains(lines[0], "failed to reconcile Deployment") {
		t.Fatalf("lines = %v", lines)
	}
}

// A failed patch while releasing a legacy key Secret is returned, and the
// Secret keeps its owner reference for the next reconcile.
func TestReconcileAtrestKey_ReleasePatchFailure(t *testing.T) {
	inst := newInstance("legacy", "default")
	isController := true
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "legacy-atrest-key",
			Namespace:       "default",
			OwnerReferences: []metav1.OwnerReference{{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "Instance", Name: inst.Name, UID: inst.UID, Controller: &isController}},
		},
		Data: map[string][]byte{"encryption-key": []byte("k")},
	}
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(inst, secret).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if _, ok := obj.(*corev1.Secret); ok {
					return errors.NewForbidden(schema.GroupResource{Resource: "secrets"}, obj.GetName(), fmt.Errorf("denied"))
				}
				return c.Patch(ctx, obj, patch, opts...)
			},
		}).Build()
	r := &InstanceReconciler{Client: c, Scheme: newScheme()}
	if err := r.reconcileAtrestKey(context.Background(), inst); err == nil {
		t.Fatal("the failed release was not reported")
	}
	var got corev1.Secret
	if err := c.Get(context.Background(), types.NamespacedName{Name: secret.Name, Namespace: secret.Namespace}, &got); err != nil || len(got.OwnerReferences) != 1 {
		t.Fatalf("owner references = %v (err %v), want unchanged", got.OwnerReferences, err)
	}
}
