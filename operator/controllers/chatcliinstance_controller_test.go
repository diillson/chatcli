package controllers

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	chatcliv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = chatcliv1alpha1.AddToScheme(s)
	return s
}

func newInstance(name, ns string) *chatcliv1alpha1.ChatCLIInstance {
	return &chatcliv1alpha1.ChatCLIInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  ns,
			UID:        "test-uid",
			Generation: 1,
		},
		Spec: chatcliv1alpha1.ChatCLIInstanceSpec{
			Provider: "CLAUDEAI",
			Model:    "claude-sonnet-4-5",
		},
	}
}

func int32Ptr(i int32) *int32 { return &i }
func strPtr(s string) *string { return &s }

func TestLabels(t *testing.T) {
	instance := newInstance("my-chatcli", "default")
	l := labels(instance)

	if l["app.kubernetes.io/name"] != "chatcli" {
		t.Errorf("expected name label 'chatcli', got %q", l["app.kubernetes.io/name"])
	}
	if l["app.kubernetes.io/instance"] != "my-chatcli" {
		t.Errorf("expected instance label 'my-chatcli', got %q", l["app.kubernetes.io/instance"])
	}
	if l["app.kubernetes.io/managed-by"] != "chatcli-operator" {
		t.Errorf("expected managed-by label 'chatcli-operator', got %q", l["app.kubernetes.io/managed-by"])
	}
}

func TestBuildContainerArgs_Basic(t *testing.T) {
	s := newScheme()
	r := &ChatCLIInstanceReconciler{Scheme: s}
	instance := newInstance("test", "default")

	args := r.buildContainerArgs(instance)

	// Should have --port 50051 (default) and --provider CLAUDEAI and --model
	expectedArgs := map[string]string{
		"--port":     "50051",
		"--provider": "CLAUDEAI",
		"--model":    "claude-sonnet-4-5",
	}

	for i := 0; i < len(args)-1; i += 2 {
		if expected, ok := expectedArgs[args[i]]; ok {
			if args[i+1] != expected {
				t.Errorf("expected %s %s, got %s %s", args[i], expected, args[i], args[i+1])
			}
			delete(expectedArgs, args[i])
		}
	}
	for k, v := range expectedArgs {
		t.Errorf("missing expected arg %s %s", k, v)
	}
}

func TestBuildContainerArgs_WithTLS(t *testing.T) {
	s := newScheme()
	r := &ChatCLIInstanceReconciler{Scheme: s}
	instance := newInstance("test", "default")
	instance.Spec.Server.TLS = &chatcliv1alpha1.TLSSpec{
		Enabled:    true,
		SecretName: "my-tls",
	}

	args := r.buildContainerArgs(instance)

	foundCert := false
	foundKey := false
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--tls-cert" {
			foundCert = true
			if args[i+1] != "/etc/chatcli/tls/tls.crt" {
				t.Errorf("unexpected tls-cert path: %s", args[i+1])
			}
		}
		if args[i] == "--tls-key" {
			foundKey = true
			if args[i+1] != "/etc/chatcli/tls/tls.key" {
				t.Errorf("unexpected tls-key path: %s", args[i+1])
			}
		}
	}
	if !foundCert || !foundKey {
		t.Error("expected --tls-cert and --tls-key args when TLS is enabled")
	}
}

func TestBuildContainerArgs_WithWatcher(t *testing.T) {
	s := newScheme()
	r := &ChatCLIInstanceReconciler{Scheme: s}
	instance := newInstance("test", "default")
	instance.Spec.Watcher = &chatcliv1alpha1.WatcherSpec{
		Enabled:     true,
		Deployment:  "my-app",
		Namespace:   "production",
		Interval:    "1m",
		Window:      "30m",
		MaxLogLines: 200,
	}

	args := r.buildContainerArgs(instance)

	expected := map[string]string{
		"--watch-deployment":    "my-app",
		"--watch-namespace":     "production",
		"--watch-interval":      "1m",
		"--watch-window":        "30m",
		"--watch-max-log-lines": "200",
	}

	for i := 0; i < len(args)-1; i++ {
		if val, ok := expected[args[i]]; ok {
			if args[i+1] != val {
				t.Errorf("expected %s %s, got %s", args[i], val, args[i+1])
			}
			delete(expected, args[i])
		}
	}
	for k, v := range expected {
		t.Errorf("missing expected watcher arg %s %s", k, v)
	}
}

func TestBuildPodSpec_DefaultSecurityContext(t *testing.T) {
	s := newScheme()
	r := &ChatCLIInstanceReconciler{Scheme: s}
	instance := newInstance("test", "default")

	podSpec := r.buildPodSpec(instance)

	if podSpec.SecurityContext == nil {
		t.Fatal("expected default security context")
	}
	if podSpec.SecurityContext.RunAsNonRoot == nil || !*podSpec.SecurityContext.RunAsNonRoot {
		t.Error("expected RunAsNonRoot=true")
	}
	if podSpec.SecurityContext.RunAsUser == nil || *podSpec.SecurityContext.RunAsUser != 1000 {
		t.Error("expected RunAsUser=1000")
	}
	if podSpec.SecurityContext.SeccompProfile == nil || podSpec.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Error("expected SeccompProfile RuntimeDefault")
	}
}

func TestBuildPodSpec_CustomSecurityContext(t *testing.T) {
	s := newScheme()
	r := &ChatCLIInstanceReconciler{Scheme: s}
	instance := newInstance("test", "default")
	instance.Spec.SecurityContext = &corev1.PodSecurityContext{
		RunAsUser: int64Ptr(2000),
	}

	podSpec := r.buildPodSpec(instance)

	if podSpec.SecurityContext.RunAsUser == nil || *podSpec.SecurityContext.RunAsUser != 2000 {
		t.Errorf("expected custom RunAsUser=2000, got %v", podSpec.SecurityContext.RunAsUser)
	}
}

func TestBuildPodSpec_Volumes(t *testing.T) {
	s := newScheme()
	r := &ChatCLIInstanceReconciler{Scheme: s}
	instance := newInstance("test", "default")
	instance.Spec.Persistence = &chatcliv1alpha1.PersistenceSpec{
		Enabled: true,
		Size:    "5Gi",
	}
	instance.Spec.Server.TLS = &chatcliv1alpha1.TLSSpec{
		Enabled:    true,
		SecretName: "tls-secret",
	}

	podSpec := r.buildPodSpec(instance)

	// Expect 3 volumes: tmp, sessions, tls
	if len(podSpec.Volumes) != 3 {
		t.Fatalf("expected 3 volumes, got %d", len(podSpec.Volumes))
	}

	volumeNames := map[string]bool{}
	for _, v := range podSpec.Volumes {
		volumeNames[v.Name] = true
	}
	for _, name := range []string{"tmp", "sessions", "tls"} {
		if !volumeNames[name] {
			t.Errorf("missing expected volume %q", name)
		}
	}

	// Check container volume mounts
	container := podSpec.Containers[0]
	if len(container.VolumeMounts) != 3 {
		t.Fatalf("expected 3 volume mounts, got %d", len(container.VolumeMounts))
	}
}

func TestBuildPodSpec_Container(t *testing.T) {
	s := newScheme()
	r := &ChatCLIInstanceReconciler{Scheme: s}
	instance := newInstance("test", "default")
	instance.Spec.Image.Repository = "my-registry/chatcli"
	instance.Spec.Image.Tag = "v1.0.0"
	instance.Spec.Server.Port = 8080
	instance.Spec.APIKeys = &chatcliv1alpha1.SecretRefSpec{Name: "api-keys"}

	podSpec := r.buildPodSpec(instance)

	c := podSpec.Containers[0]
	if c.Name != "chatcli" {
		t.Errorf("expected container name 'chatcli', got %q", c.Name)
	}
	if c.Image != "my-registry/chatcli:v1.0.0" {
		t.Errorf("expected image 'my-registry/chatcli:v1.0.0', got %q", c.Image)
	}
	if len(c.Ports) != 1 || c.Ports[0].ContainerPort != 8080 {
		t.Errorf("expected port 8080, got %v", c.Ports)
	}
	if c.SecurityContext == nil || c.SecurityContext.AllowPrivilegeEscalation == nil || *c.SecurityContext.AllowPrivilegeEscalation {
		t.Error("expected AllowPrivilegeEscalation=false")
	}

	// Should have ConfigMap + Secret EnvFrom
	if len(c.EnvFrom) != 2 {
		t.Fatalf("expected 2 envFrom sources, got %d", len(c.EnvFrom))
	}
	if c.EnvFrom[0].ConfigMapRef == nil || c.EnvFrom[0].ConfigMapRef.Name != "test" {
		t.Error("expected ConfigMap envFrom")
	}
	if c.EnvFrom[1].SecretRef == nil || c.EnvFrom[1].SecretRef.Name != "api-keys" {
		t.Error("expected Secret envFrom for API keys")
	}
}

// Tests using fake client for reconciliation

func setupFakeReconciler(objs ...client.Object) (*ChatCLIInstanceReconciler, client.Client) {
	s := newScheme()
	cb := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&chatcliv1alpha1.ChatCLIInstance{})
	if len(objs) > 0 {
		cb = cb.WithObjects(objs...)
	}
	c := cb.Build()
	return &ChatCLIInstanceReconciler{Client: c, Scheme: s}, c
}

func TestReconcile_NotFound(t *testing.T) {
	r, _ := setupFakeReconciler()
	ctx := context.Background()

	result, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nonexistent", Namespace: "default"},
	})

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.Requeue {
		t.Error("expected no requeue")
	}
}

func TestReconcile_CreatesResources(t *testing.T) {
	instance := newInstance("test-chatcli", "default")
	r, c := setupFakeReconciler(instance)
	ctx := context.Background()

	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-chatcli", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	// Check finalizer was added
	var updated chatcliv1alpha1.ChatCLIInstance
	if err := c.Get(ctx, types.NamespacedName{Name: "test-chatcli", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated instance: %v", err)
	}
	hasFinalizer := false
	for _, f := range updated.Finalizers {
		if f == finalizerName {
			hasFinalizer = true
			break
		}
	}
	if !hasFinalizer {
		t.Error("expected finalizer to be added")
	}

	// Reconcile again (now with finalizer) to create resources
	_, err = r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-chatcli", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("second reconcile failed: %v", err)
	}

	// Verify ServiceAccount
	var sa corev1.ServiceAccount
	if err := c.Get(ctx, types.NamespacedName{Name: "test-chatcli", Namespace: "default"}, &sa); err != nil {
		t.Errorf("expected ServiceAccount to be created: %v", err)
	}

	// Verify ConfigMap
	var cm corev1.ConfigMap
	if err := c.Get(ctx, types.NamespacedName{Name: "test-chatcli", Namespace: "default"}, &cm); err != nil {
		t.Errorf("expected ConfigMap to be created: %v", err)
	} else {
		if cm.Data["LLM_PROVIDER"] != "CLAUDEAI" {
			t.Errorf("expected LLM_PROVIDER=CLAUDEAI, got %q", cm.Data["LLM_PROVIDER"])
		}
		if cm.Data["LLM_MODEL"] != "claude-sonnet-4-5" {
			t.Errorf("expected LLM_MODEL=claude-sonnet-4-5, got %q", cm.Data["LLM_MODEL"])
		}
	}

	// Verify Service
	var svc corev1.Service
	if err := c.Get(ctx, types.NamespacedName{Name: "test-chatcli", Namespace: "default"}, &svc); err != nil {
		t.Errorf("expected Service to be created: %v", err)
	} else {
		if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != 50051 {
			t.Errorf("expected port 50051, got %v", svc.Spec.Ports)
		}
	}

	// Verify Deployment
	var deploy appsv1.Deployment
	if err := c.Get(ctx, types.NamespacedName{Name: "test-chatcli", Namespace: "default"}, &deploy); err != nil {
		t.Errorf("expected Deployment to be created: %v", err)
	} else {
		if *deploy.Spec.Replicas != 1 {
			t.Errorf("expected 1 replica, got %d", *deploy.Spec.Replicas)
		}
	}

	// Verify NO RBAC (watcher not enabled)
	var role rbacv1.Role
	if err := c.Get(ctx, types.NamespacedName{Name: "test-chatcli-watcher", Namespace: "default"}, &role); !errors.IsNotFound(err) {
		t.Error("expected no Role when watcher is disabled")
	}

	// Verify NO PVC (persistence not enabled)
	var pvc corev1.PersistentVolumeClaim
	if err := c.Get(ctx, types.NamespacedName{Name: "test-chatcli-sessions", Namespace: "default"}, &pvc); !errors.IsNotFound(err) {
		t.Error("expected no PVC when persistence is disabled")
	}
}

func TestReconcile_WithWatcher(t *testing.T) {
	instance := newInstance("test-watcher", "default")
	instance.Spec.Watcher = &chatcliv1alpha1.WatcherSpec{
		Enabled:    true,
		Deployment: "my-app",
		Namespace:  "production",
	}
	r, c := setupFakeReconciler(instance)
	ctx := context.Background()

	// First reconcile: add finalizer
	_, _ = r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-watcher", Namespace: "default"},
	})
	// Second reconcile: create resources
	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-watcher", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	// Verify Role
	var role rbacv1.Role
	if err := c.Get(ctx, types.NamespacedName{Name: "test-watcher-watcher", Namespace: "default"}, &role); err != nil {
		t.Fatalf("expected Role to be created: %v", err)
	}
	if len(role.Rules) == 0 {
		t.Error("expected Role rules to be populated")
	}

	// Verify RoleBinding
	var rb rbacv1.RoleBinding
	if err := c.Get(ctx, types.NamespacedName{Name: "test-watcher-watcher", Namespace: "default"}, &rb); err != nil {
		t.Fatalf("expected RoleBinding to be created: %v", err)
	}
	if rb.RoleRef.Name != "test-watcher-watcher" {
		t.Errorf("expected RoleRef to reference 'test-watcher-watcher', got %q", rb.RoleRef.Name)
	}

	// Verify ConfigMap has watcher env vars
	var cm corev1.ConfigMap
	if err := c.Get(ctx, types.NamespacedName{Name: "test-watcher", Namespace: "default"}, &cm); err != nil {
		t.Fatalf("failed to get ConfigMap: %v", err)
	}
	if cm.Data["CHATCLI_WATCH_DEPLOYMENT"] != "my-app" {
		t.Errorf("expected CHATCLI_WATCH_DEPLOYMENT=my-app, got %q", cm.Data["CHATCLI_WATCH_DEPLOYMENT"])
	}
	if cm.Data["CHATCLI_WATCH_NAMESPACE"] != "production" {
		t.Errorf("expected CHATCLI_WATCH_NAMESPACE=production, got %q", cm.Data["CHATCLI_WATCH_NAMESPACE"])
	}
}

func TestReconcile_WithPersistence(t *testing.T) {
	instance := newInstance("test-persist", "default")
	instance.Spec.Persistence = &chatcliv1alpha1.PersistenceSpec{
		Enabled:          true,
		Size:             "5Gi",
		StorageClassName: strPtr("fast-ssd"),
	}
	r, c := setupFakeReconciler(instance)
	ctx := context.Background()

	// First reconcile: add finalizer
	_, _ = r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-persist", Namespace: "default"},
	})
	// Second reconcile: create resources
	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-persist", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	// Verify PVC
	var pvc corev1.PersistentVolumeClaim
	if err := c.Get(ctx, types.NamespacedName{Name: "test-persist-sessions", Namespace: "default"}, &pvc); err != nil {
		t.Fatalf("expected PVC to be created: %v", err)
	}

	expectedSize := resource.MustParse("5Gi")
	if pvc.Spec.Resources.Requests[corev1.ResourceStorage] != expectedSize {
		t.Errorf("expected PVC size 5Gi, got %v", pvc.Spec.Resources.Requests[corev1.ResourceStorage])
	}
	if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != "fast-ssd" {
		t.Error("expected StorageClassName 'fast-ssd'")
	}
}

func TestReconcile_WithReplicas(t *testing.T) {
	instance := newInstance("test-replicas", "default")
	instance.Spec.Replicas = int32Ptr(3)
	r, c := setupFakeReconciler(instance)
	ctx := context.Background()

	// First + second reconcile
	_, _ = r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-replicas", Namespace: "default"},
	})
	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-replicas", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	var deploy appsv1.Deployment
	if err := c.Get(ctx, types.NamespacedName{Name: "test-replicas", Namespace: "default"}, &deploy); err != nil {
		t.Fatalf("failed to get Deployment: %v", err)
	}
	if *deploy.Spec.Replicas != 3 {
		t.Errorf("expected 3 replicas, got %d", *deploy.Spec.Replicas)
	}
}

func TestReconcile_Deletion(t *testing.T) {
	now := metav1.NewTime(time.Now())
	instance := newInstance("test-delete", "default")
	instance.DeletionTimestamp = &now
	instance.Finalizers = []string{finalizerName}

	r, c := setupFakeReconciler(instance)
	ctx := context.Background()

	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-delete", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile deletion failed: %v", err)
	}

	// The fake client garbage-collects objects with DeletionTimestamp and no finalizers,
	// so the object should be gone after the finalizer is removed.
	var updated chatcliv1alpha1.ChatCLIInstance
	err = c.Get(ctx, types.NamespacedName{Name: "test-delete", Namespace: "default"}, &updated)
	if err == nil {
		// Object still exists - verify finalizer was removed
		for _, f := range updated.Finalizers {
			if f == finalizerName {
				t.Error("expected finalizer to be removed after deletion")
			}
		}
	} else if !errors.IsNotFound(err) {
		t.Fatalf("unexpected error getting instance: %v", err)
	}
	// IsNotFound is expected - object was garbage collected after finalizer removal
}

func TestReconcile_CustomPort(t *testing.T) {
	instance := newInstance("test-port", "default")
	instance.Spec.Server.Port = 9090
	r, c := setupFakeReconciler(instance)
	ctx := context.Background()

	_, _ = r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-port", Namespace: "default"},
	})
	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-port", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	// Verify Service port
	var svc corev1.Service
	if err := c.Get(ctx, types.NamespacedName{Name: "test-port", Namespace: "default"}, &svc); err != nil {
		t.Fatalf("failed to get Service: %v", err)
	}
	if svc.Spec.Ports[0].Port != 9090 {
		t.Errorf("expected service port 9090, got %d", svc.Spec.Ports[0].Port)
	}

	// Verify ConfigMap
	var cm corev1.ConfigMap
	if err := c.Get(ctx, types.NamespacedName{Name: "test-port", Namespace: "default"}, &cm); err != nil {
		t.Fatalf("failed to get ConfigMap: %v", err)
	}
	if cm.Data["CHATCLI_SERVER_PORT"] != "9090" {
		t.Errorf("expected CHATCLI_SERVER_PORT=9090, got %q", cm.Data["CHATCLI_SERVER_PORT"])
	}
}

func TestHelpers(t *testing.T) {
	b := boolPtr(true)
	if !*b {
		t.Error("boolPtr(true) should return *true")
	}

	i := int64Ptr(42)
	if *i != 42 {
		t.Errorf("int64Ptr(42) should return *42, got %d", *i)
	}

	q := resourceQuantity("100Mi")
	expected := resource.MustParse("100Mi")
	if !q.Equal(expected) {
		t.Errorf("resourceQuantity('100Mi') should return 100Mi, got %s", q.String())
	}
}

func TestDeepCopy(t *testing.T) {
	instance := newInstance("test-dc", "default")
	instance.Spec.Replicas = int32Ptr(3)
	instance.Spec.Watcher = &chatcliv1alpha1.WatcherSpec{
		Enabled:    true,
		Deployment: "app",
		Namespace:  "prod",
	}
	instance.Spec.Persistence = &chatcliv1alpha1.PersistenceSpec{
		Enabled:          true,
		Size:             "2Gi",
		StorageClassName: strPtr("standard"),
	}
	instance.Spec.SecurityContext = &corev1.PodSecurityContext{
		RunAsUser: int64Ptr(1000),
	}
	instance.Spec.APIKeys = &chatcliv1alpha1.SecretRefSpec{Name: "keys"}
	instance.Spec.Server.TLS = &chatcliv1alpha1.TLSSpec{
		Enabled:    true,
		SecretName: "tls",
	}
	instance.Spec.Server.Token = &chatcliv1alpha1.SecretKeyRefSpec{
		Name: "auth",
		Key:  "token",
	}
	instance.Status.Conditions = []metav1.Condition{
		{Type: "Available", Status: metav1.ConditionTrue, Reason: "test",
			Message: "ok", LastTransitionTime: metav1.Now()},
	}

	// DeepCopy should not panic and should produce independent copy
	copy := instance.DeepCopy()
	if copy == nil {
		t.Fatal("DeepCopy returned nil")
	}
	if copy.Name != instance.Name {
		t.Error("DeepCopy name mismatch")
	}

	// Mutate copy, ensure original unchanged
	*copy.Spec.Replicas = 10
	if *instance.Spec.Replicas == 10 {
		t.Error("DeepCopy did not produce independent copy for Replicas")
	}

	copy.Spec.Watcher.Deployment = "changed"
	if instance.Spec.Watcher.Deployment == "changed" {
		t.Error("DeepCopy did not produce independent copy for Watcher")
	}

	// DeepCopyObject
	obj := instance.DeepCopyObject()
	if obj == nil {
		t.Error("DeepCopyObject returned nil")
	}

	// List deep copy
	list := &chatcliv1alpha1.ChatCLIInstanceList{
		Items: []chatcliv1alpha1.ChatCLIInstance{*instance},
	}
	listCopy := list.DeepCopy()
	if listCopy == nil || len(listCopy.Items) != 1 {
		t.Error("list DeepCopy failed")
	}
	listObj := list.DeepCopyObject()
	if listObj == nil {
		t.Error("list DeepCopyObject returned nil")
	}

	// Nil deep copies
	var nilInstance *chatcliv1alpha1.ChatCLIInstance
	if nilInstance.DeepCopy() != nil {
		t.Error("nil DeepCopy should return nil")
	}
	var nilSpec *chatcliv1alpha1.ChatCLIInstanceSpec
	if nilSpec.DeepCopy() != nil {
		t.Error("nil spec DeepCopy should return nil")
	}
}

// --- Multi-target watcher tests ---

func TestBuildContainerArgs_WithMultiTargetWatcher(t *testing.T) {
	s := newScheme()
	r := &ChatCLIInstanceReconciler{Scheme: s}
	instance := newInstance("test", "default")
	instance.Spec.Watcher = &chatcliv1alpha1.WatcherSpec{
		Enabled:  true,
		Interval: "1m",
		Window:   "30m",
		Targets: []chatcliv1alpha1.WatchTargetSpec{
			{Deployment: "app-a", Namespace: "ns-a"},
			{Deployment: "app-b", Namespace: "ns-b"},
		},
	}

	args := r.buildContainerArgs(instance)

	// Should include --watch-config pointing to the config file
	foundWatchConfig := false
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--watch-config" {
			foundWatchConfig = true
			if args[i+1] != "/etc/chatcli/watch/watch-config.yaml" {
				t.Errorf("expected --watch-config path '/etc/chatcli/watch/watch-config.yaml', got %q", args[i+1])
			}
		}
		// Should NOT have legacy single-target flags
		if args[i] == "--watch-deployment" {
			t.Error("should not have --watch-deployment when Targets is set")
		}
		if args[i] == "--watch-namespace" {
			t.Error("should not have --watch-namespace when Targets is set")
		}
		if args[i] == "--watch-interval" {
			t.Error("should not have --watch-interval when Targets is set (interval goes in config YAML)")
		}
		if args[i] == "--watch-window" {
			t.Error("should not have --watch-window when Targets is set (window goes in config YAML)")
		}
	}
	if !foundWatchConfig {
		t.Error("expected --watch-config arg when Targets is set")
	}
}

func TestBuildPodSpec_WatchConfigVolume(t *testing.T) {
	s := newScheme()
	r := &ChatCLIInstanceReconciler{Scheme: s}
	instance := newInstance("my-watcher", "default")
	instance.Spec.Watcher = &chatcliv1alpha1.WatcherSpec{
		Enabled: true,
		Targets: []chatcliv1alpha1.WatchTargetSpec{
			{Deployment: "frontend", Namespace: "prod"},
		},
	}

	podSpec := r.buildPodSpec(instance)

	// Check that "watch-config" volume exists
	foundVolume := false
	for _, v := range podSpec.Volumes {
		if v.Name == "watch-config" {
			foundVolume = true
			if v.VolumeSource.ConfigMap == nil {
				t.Fatal("watch-config volume should have ConfigMap source")
			}
			expectedCMName := "my-watcher-watch-config"
			if v.VolumeSource.ConfigMap.Name != expectedCMName {
				t.Errorf("expected ConfigMap name %q, got %q", expectedCMName, v.VolumeSource.ConfigMap.Name)
			}
		}
	}
	if !foundVolume {
		t.Error("expected 'watch-config' volume to be present when multi-target watcher is enabled")
	}

	// Check that container has the corresponding volume mount
	container := podSpec.Containers[0]
	foundMount := false
	for _, vm := range container.VolumeMounts {
		if vm.Name == "watch-config" {
			foundMount = true
			if vm.MountPath != "/etc/chatcli/watch" {
				t.Errorf("expected mount path '/etc/chatcli/watch', got %q", vm.MountPath)
			}
			if !vm.ReadOnly {
				t.Error("expected watch-config volume mount to be read-only")
			}
		}
	}
	if !foundMount {
		t.Error("expected 'watch-config' volume mount on the container")
	}
}

func TestBuildWatchConfigYAML(t *testing.T) {
	watcher := &chatcliv1alpha1.WatcherSpec{
		Enabled:         true,
		Interval:        "45s",
		Window:          "1h",
		MaxLogLines:     500,
		MaxContextChars: 12000,
		Targets: []chatcliv1alpha1.WatchTargetSpec{
			{
				Deployment:    "api-server",
				Namespace:     "production",
				MetricsPort:   9090,
				MetricsPath:   "/metrics",
				MetricsFilter: []string{"http_requests_*", "error_rate_*"},
			},
			{
				Deployment: "worker",
				Namespace:  "production",
			},
			{
				Deployment:  "gateway",
				Namespace:   "",
				MetricsPort: 8080,
			},
		},
	}

	yaml := buildWatchConfigYAML(watcher)

	// Verify global fields
	if !strings.Contains(yaml, `interval: "45s"`) {
		t.Errorf("expected interval field in YAML, got:\n%s", yaml)
	}
	if !strings.Contains(yaml, `window: "1h"`) {
		t.Errorf("expected window field in YAML, got:\n%s", yaml)
	}
	if !strings.Contains(yaml, "maxLogLines: 500") {
		t.Errorf("expected maxLogLines field in YAML, got:\n%s", yaml)
	}
	if !strings.Contains(yaml, "maxContextChars: 12000") {
		t.Errorf("expected maxContextChars field in YAML, got:\n%s", yaml)
	}

	// Verify targets section
	if !strings.Contains(yaml, "targets:") {
		t.Fatalf("expected targets section in YAML, got:\n%s", yaml)
	}

	// First target
	if !strings.Contains(yaml, `deployment: "api-server"`) {
		t.Errorf("expected api-server deployment in YAML, got:\n%s", yaml)
	}
	if !strings.Contains(yaml, `namespace: "production"`) {
		t.Errorf("expected production namespace in YAML, got:\n%s", yaml)
	}
	if !strings.Contains(yaml, "metricsPort: 9090") {
		t.Errorf("expected metricsPort 9090 in YAML, got:\n%s", yaml)
	}
	if !strings.Contains(yaml, `metricsPath: "/metrics"`) {
		t.Errorf("expected metricsPath /metrics in YAML, got:\n%s", yaml)
	}
	if !strings.Contains(yaml, "metricsFilter:") {
		t.Errorf("expected metricsFilter section in YAML, got:\n%s", yaml)
	}
	if !strings.Contains(yaml, `"http_requests_*"`) {
		t.Errorf("expected http_requests_* filter in YAML, got:\n%s", yaml)
	}
	if !strings.Contains(yaml, `"error_rate_*"`) {
		t.Errorf("expected error_rate_* filter in YAML, got:\n%s", yaml)
	}

	// Second target (worker, no metrics)
	if !strings.Contains(yaml, `deployment: "worker"`) {
		t.Errorf("expected worker deployment in YAML, got:\n%s", yaml)
	}

	// Third target (gateway, empty namespace defaults to "default")
	if !strings.Contains(yaml, `deployment: "gateway"`) {
		t.Errorf("expected gateway deployment in YAML, got:\n%s", yaml)
	}
	if !strings.Contains(yaml, `namespace: "default"`) {
		t.Errorf("expected default namespace for empty namespace target in YAML, got:\n%s", yaml)
	}
	if !strings.Contains(yaml, "metricsPort: 8080") {
		t.Errorf("expected metricsPort 8080 in YAML, got:\n%s", yaml)
	}

	// Verify zero-value fields are omitted
	yamlLines := strings.Split(yaml, "\n")
	workerSection := false
	for _, line := range yamlLines {
		if strings.Contains(line, `"worker"`) {
			workerSection = true
		}
		if workerSection && strings.Contains(line, "metricsPort") {
			// This could be the gateway section, only fail if it's 0
			if strings.Contains(line, "metricsPort: 0") {
				t.Error("should not output metricsPort: 0 for worker target")
			}
		}
	}
}

func TestNeedsClusterRBAC(t *testing.T) {
	tests := []struct {
		name     string
		instance *chatcliv1alpha1.ChatCLIInstance
		want     bool
	}{
		{
			name: "single target",
			instance: func() *chatcliv1alpha1.ChatCLIInstance {
				inst := newInstance("test", "default")
				inst.Spec.Watcher = &chatcliv1alpha1.WatcherSpec{
					Enabled: true,
					Targets: []chatcliv1alpha1.WatchTargetSpec{
						{Deployment: "app", Namespace: "prod"},
					},
				}
				return inst
			}(),
			want: false,
		},
		{
			name: "multiple targets same namespace",
			instance: func() *chatcliv1alpha1.ChatCLIInstance {
				inst := newInstance("test", "default")
				inst.Spec.Watcher = &chatcliv1alpha1.WatcherSpec{
					Enabled: true,
					Targets: []chatcliv1alpha1.WatchTargetSpec{
						{Deployment: "app-a", Namespace: "prod"},
						{Deployment: "app-b", Namespace: "prod"},
						{Deployment: "app-c", Namespace: "prod"},
					},
				}
				return inst
			}(),
			want: false,
		},
		{
			name: "multiple targets different namespaces",
			instance: func() *chatcliv1alpha1.ChatCLIInstance {
				inst := newInstance("test", "default")
				inst.Spec.Watcher = &chatcliv1alpha1.WatcherSpec{
					Enabled: true,
					Targets: []chatcliv1alpha1.WatchTargetSpec{
						{Deployment: "app-a", Namespace: "prod"},
						{Deployment: "app-b", Namespace: "staging"},
					},
				}
				return inst
			}(),
			want: true,
		},
		{
			name: "no watcher",
			instance: func() *chatcliv1alpha1.ChatCLIInstance {
				return newInstance("test", "default")
			}(),
			want: false,
		},
		{
			name: "no targets",
			instance: func() *chatcliv1alpha1.ChatCLIInstance {
				inst := newInstance("test", "default")
				inst.Spec.Watcher = &chatcliv1alpha1.WatcherSpec{
					Enabled:    true,
					Deployment: "legacy-app",
					Namespace:  "prod",
				}
				return inst
			}(),
			want: false,
		},
		{
			name: "multiple targets with empty namespace defaults to same",
			instance: func() *chatcliv1alpha1.ChatCLIInstance {
				inst := newInstance("test", "default")
				inst.Spec.Watcher = &chatcliv1alpha1.WatcherSpec{
					Enabled: true,
					Targets: []chatcliv1alpha1.WatchTargetSpec{
						{Deployment: "app-a", Namespace: ""},
						{Deployment: "app-b", Namespace: "default"},
					},
				}
				return inst
			}(),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := needsClusterRBAC(tt.instance)
			if got != tt.want {
				t.Errorf("needsClusterRBAC() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReconcile_WithMultiTargetWatcher(t *testing.T) {
	instance := newInstance("multi-watch", "default")
	instance.Spec.Watcher = &chatcliv1alpha1.WatcherSpec{
		Enabled:         true,
		Interval:        "30s",
		Window:          "2h",
		MaxLogLines:     200,
		MaxContextChars: 10000,
		Targets: []chatcliv1alpha1.WatchTargetSpec{
			{
				Deployment:    "api",
				Namespace:     "production",
				MetricsPort:   9090,
				MetricsPath:   "/metrics",
				MetricsFilter: []string{"http_*"},
			},
			{
				Deployment: "worker",
				Namespace:  "production",
			},
		},
	}

	r, c := setupFakeReconciler(instance)
	ctx := context.Background()

	// First reconcile: add finalizer
	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "multi-watch", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("first reconcile failed: %v", err)
	}

	// Second reconcile: create resources
	_, err = r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "multi-watch", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("second reconcile failed: %v", err)
	}

	// 1. Verify watch-config ConfigMap was created
	var watchCM corev1.ConfigMap
	if err := c.Get(ctx, types.NamespacedName{Name: "multi-watch-watch-config", Namespace: "default"}, &watchCM); err != nil {
		t.Fatalf("expected watch-config ConfigMap to be created: %v", err)
	}

	yamlData, ok := watchCM.Data["watch-config.yaml"]
	if !ok {
		t.Fatal("expected 'watch-config.yaml' key in watch-config ConfigMap")
	}

	// Verify YAML content
	if !strings.Contains(yamlData, `interval: "30s"`) {
		t.Errorf("expected interval in watch config YAML, got:\n%s", yamlData)
	}
	if !strings.Contains(yamlData, `window: "2h"`) {
		t.Errorf("expected window in watch config YAML, got:\n%s", yamlData)
	}
	if !strings.Contains(yamlData, "maxLogLines: 200") {
		t.Errorf("expected maxLogLines in watch config YAML, got:\n%s", yamlData)
	}
	if !strings.Contains(yamlData, "maxContextChars: 10000") {
		t.Errorf("expected maxContextChars in watch config YAML, got:\n%s", yamlData)
	}
	if !strings.Contains(yamlData, `deployment: "api"`) {
		t.Errorf("expected api deployment in watch config YAML, got:\n%s", yamlData)
	}
	if !strings.Contains(yamlData, `deployment: "worker"`) {
		t.Errorf("expected worker deployment in watch config YAML, got:\n%s", yamlData)
	}

	// 2. Verify Deployment container args include --watch-config
	var deploy appsv1.Deployment
	if err := c.Get(ctx, types.NamespacedName{Name: "multi-watch", Namespace: "default"}, &deploy); err != nil {
		t.Fatalf("expected Deployment to be created: %v", err)
	}

	containerArgs := deploy.Spec.Template.Spec.Containers[0].Args
	foundWatchConfig := false
	for i := 0; i < len(containerArgs)-1; i++ {
		if containerArgs[i] == "--watch-config" {
			foundWatchConfig = true
			if containerArgs[i+1] != "/etc/chatcli/watch/watch-config.yaml" {
				t.Errorf("unexpected --watch-config value: %s", containerArgs[i+1])
			}
		}
	}
	if !foundWatchConfig {
		t.Errorf("expected --watch-config in container args, got: %v", containerArgs)
	}

	// 3. Verify watch-config volume is mounted
	volumes := deploy.Spec.Template.Spec.Volumes
	foundVolume := false
	for _, v := range volumes {
		if v.Name == "watch-config" {
			foundVolume = true
			if v.VolumeSource.ConfigMap == nil || v.VolumeSource.ConfigMap.Name != "multi-watch-watch-config" {
				t.Errorf("expected watch-config volume to reference ConfigMap 'multi-watch-watch-config'")
			}
		}
	}
	if !foundVolume {
		t.Error("expected 'watch-config' volume in pod spec")
	}

	mounts := deploy.Spec.Template.Spec.Containers[0].VolumeMounts
	foundMount := false
	for _, vm := range mounts {
		if vm.Name == "watch-config" {
			foundMount = true
			if vm.MountPath != "/etc/chatcli/watch" {
				t.Errorf("expected mount path '/etc/chatcli/watch', got %q", vm.MountPath)
			}
		}
	}
	if !foundMount {
		t.Error("expected 'watch-config' volume mount in container")
	}
}

func TestDeepCopy_WatcherWithTargets(t *testing.T) {
	instance := newInstance("test-dc-multi", "default")
	instance.Spec.Watcher = &chatcliv1alpha1.WatcherSpec{
		Enabled:         true,
		Interval:        "30s",
		Window:          "1h",
		MaxLogLines:     100,
		MaxContextChars: 8000,
		Targets: []chatcliv1alpha1.WatchTargetSpec{
			{
				Deployment:    "api",
				Namespace:     "prod",
				MetricsPort:   9090,
				MetricsPath:   "/metrics",
				MetricsFilter: []string{"http_*", "grpc_*"},
			},
			{
				Deployment: "worker",
				Namespace:  "staging",
			},
		},
	}

	// DeepCopy
	cp := instance.DeepCopy()
	if cp == nil {
		t.Fatal("DeepCopy returned nil")
	}

	// Verify the copy has the same values
	if len(cp.Spec.Watcher.Targets) != 2 {
		t.Fatalf("expected 2 targets in copy, got %d", len(cp.Spec.Watcher.Targets))
	}
	if cp.Spec.Watcher.Targets[0].Deployment != "api" {
		t.Errorf("expected first target deployment 'api', got %q", cp.Spec.Watcher.Targets[0].Deployment)
	}
	if len(cp.Spec.Watcher.Targets[0].MetricsFilter) != 2 {
		t.Fatalf("expected 2 metrics filters in copy, got %d", len(cp.Spec.Watcher.Targets[0].MetricsFilter))
	}
	if cp.Spec.Watcher.Targets[0].MetricsFilter[0] != "http_*" {
		t.Errorf("expected first filter 'http_*', got %q", cp.Spec.Watcher.Targets[0].MetricsFilter[0])
	}

	// Mutate the copy's Targets slice - should not affect original
	cp.Spec.Watcher.Targets[0].Deployment = "mutated-api"
	if instance.Spec.Watcher.Targets[0].Deployment == "mutated-api" {
		t.Error("modifying copy's target Deployment affected the original")
	}

	cp.Spec.Watcher.Targets[0].Namespace = "mutated-ns"
	if instance.Spec.Watcher.Targets[0].Namespace == "mutated-ns" {
		t.Error("modifying copy's target Namespace affected the original")
	}

	cp.Spec.Watcher.Targets[0].MetricsPort = 1111
	if instance.Spec.Watcher.Targets[0].MetricsPort == 1111 {
		t.Error("modifying copy's target MetricsPort affected the original")
	}

	// Mutate the copy's MetricsFilter slice - should not affect original
	cp.Spec.Watcher.Targets[0].MetricsFilter[0] = "mutated_filter"
	if instance.Spec.Watcher.Targets[0].MetricsFilter[0] == "mutated_filter" {
		t.Error("modifying copy's MetricsFilter affected the original")
	}

	// Append to the copy's Targets slice - should not affect original
	cp.Spec.Watcher.Targets = append(cp.Spec.Watcher.Targets, chatcliv1alpha1.WatchTargetSpec{
		Deployment: "extra",
		Namespace:  "extra-ns",
	})
	if len(instance.Spec.Watcher.Targets) != 2 {
		t.Errorf("appending to copy's Targets affected original, original now has %d targets", len(instance.Spec.Watcher.Targets))
	}

	// Append to the copy's MetricsFilter slice - should not affect original
	cp.Spec.Watcher.Targets[1].MetricsFilter = append(cp.Spec.Watcher.Targets[1].MetricsFilter, "new_filter")
	if len(instance.Spec.Watcher.Targets[1].MetricsFilter) != 0 {
		t.Errorf("appending to copy's MetricsFilter affected original, original now has %d filters",
			len(instance.Spec.Watcher.Targets[1].MetricsFilter))
	}

	// Mutate the copy's watcher-level fields - should not affect original
	cp.Spec.Watcher.Interval = "99s"
	if instance.Spec.Watcher.Interval == "99s" {
		t.Error("modifying copy's Interval affected the original")
	}
	cp.Spec.Watcher.MaxLogLines = 9999
	if instance.Spec.Watcher.MaxLogLines == 9999 {
		t.Error("modifying copy's MaxLogLines affected the original")
	}
}
