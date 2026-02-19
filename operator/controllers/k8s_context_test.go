package controllers

import (
	"context"
	"fmt"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

func newTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = appsv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	_ = platformv1alpha1.AddToScheme(s)
	return s
}

func TestBuildContext_DeploymentStatus(t *testing.T) {
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(3)),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "api"}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "app", Image: "api:v2.0"},
					},
				},
			},
		},
		Status: appsv1.DeploymentStatus{
			ReadyReplicas:       2,
			UpdatedReplicas:     3,
			AvailableReplicas:   2,
			UnavailableReplicas: 1,
			Conditions: []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue, Reason: "MinimumReplicasAvailable"},
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(deploy).Build()
	builder := NewKubernetesContextBuilder(c)

	result, err := builder.BuildContext(context.Background(), platformv1alpha1.ResourceRef{
		Kind: "Deployment", Name: "api", Namespace: "prod",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, want := range []string{
		"## Deployment Status",
		"desired=3 ready=2 updated=3",
		"unavailable=1",
		"api:v2.0",
		"MinimumReplicasAvailable",
	} {
		if !strings.Contains(result, want) {
			t.Errorf("expected context to contain %q, got:\n%s", want, result)
		}
	}
}

func TestBuildContext_PodDetails(t *testing.T) {
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(2)),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "web:v1"}}},
			},
		},
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "web-abc-123", Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "ReplicaSet", Name: "web-abc", APIVersion: "apps/v1"},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:         "app",
					Ready:        false,
					RestartCount: 5,
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
					},
					LastTerminationState: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137},
					},
				},
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(deploy, pod).Build()
	builder := NewKubernetesContextBuilder(c)

	result, err := builder.BuildContext(context.Background(), platformv1alpha1.ResourceRef{
		Kind: "Deployment", Name: "web", Namespace: "default",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, want := range []string{
		"## Pod Details",
		"web-abc-123",
		"restarts=5",
		"CrashLoopBackOff",
		"OOMKilled",
		"exitCode=137",
	} {
		if !strings.Contains(result, want) {
			t.Errorf("expected context to contain %q, got:\n%s", want, result)
		}
	}
}

func TestBuildContext_RecentEvents(t *testing.T) {
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "ns1"},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "svc"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "svc"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: "svc:v1"}}},
			},
		},
	}

	event := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "svc-event-1", Namespace: "ns1"},
		InvolvedObject: corev1.ObjectReference{Name: "svc", Namespace: "ns1"},
		Type:           "Warning",
		Reason:         "FailedScheduling",
		Message:        "0/3 nodes available",
		Count:          4,
		LastTimestamp:   metav1.Now(),
	}

	c := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(deploy, event).Build()
	builder := NewKubernetesContextBuilder(c)

	result, err := builder.BuildContext(context.Background(), platformv1alpha1.ResourceRef{
		Kind: "Deployment", Name: "svc", Namespace: "ns1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, want := range []string{
		"## Recent Events",
		"Warning",
		"FailedScheduling",
		"0/3 nodes available",
		"count=4",
	} {
		if !strings.Contains(result, want) {
			t.Errorf("expected context to contain %q, got:\n%s", want, result)
		}
	}
}

func TestBuildContext_RevisionHistory(t *testing.T) {
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod", UID: "deploy-uid"},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(3)),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "api"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "api:v3"}}},
			},
		},
	}

	rs1 := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "api-v1", Namespace: "prod",
			Annotations:     map[string]string{"deployment.kubernetes.io/revision": "1"},
			OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "api", UID: "deploy-uid"}},
		},
		Spec: appsv1.ReplicaSetSpec{
			Replicas: ptr.To(int32(0)),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "api"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "api:v1"}}},
			},
		},
		Status: appsv1.ReplicaSetStatus{ReadyReplicas: 0},
	}

	rs2 := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "api-v2", Namespace: "prod",
			Annotations:     map[string]string{"deployment.kubernetes.io/revision": "2"},
			OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "api", UID: "deploy-uid"}},
		},
		Spec: appsv1.ReplicaSetSpec{
			Replicas: ptr.To(int32(3)),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "api"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "api:v2"}}},
			},
		},
		Status: appsv1.ReplicaSetStatus{ReadyReplicas: 3},
	}

	c := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(deploy, rs1, rs2).Build()
	builder := NewKubernetesContextBuilder(c)

	result, err := builder.BuildContext(context.Background(), platformv1alpha1.ResourceRef{
		Kind: "Deployment", Name: "api", Namespace: "prod",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, want := range []string{
		"## Revision History",
		"Revision 2",
		"Revision 1",
		"api:v2",
		"api:v1",
		"app: api:v1 → api:v2",
	} {
		if !strings.Contains(result, want) {
			t.Errorf("expected context to contain %q, got:\n%s", want, result)
		}
	}
}

func TestBuildContext_NonDeploymentKind(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newTestScheme()).Build()
	builder := NewKubernetesContextBuilder(c)

	result, err := builder.BuildContext(context.Background(), platformv1alpha1.ResourceRef{
		Kind: "StatefulSet", Name: "db", Namespace: "default",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "StatefulSet") {
		t.Errorf("expected non-deployment message, got: %s", result)
	}
}

func TestBuildContext_Truncation(t *testing.T) {
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "big", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "big"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "big"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "big:v1"}}},
			},
		},
	}

	// Create many events to generate large output
	objects := []runtime.Object{deploy}
	for i := 0; i < 100; i++ {
		objects = append(objects, &corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: fmt.Sprintf("big-event-%d", i), Namespace: "default"},
			InvolvedObject: corev1.ObjectReference{Name: "big", Namespace: "default"},
			Type:           "Warning",
			Reason:         "SomeReason",
			Message:        strings.Repeat("very long message ", 20),
			Count:          1,
			LastTimestamp:   metav1.Now(),
		})
	}

	c := fake.NewClientBuilder().WithScheme(newTestScheme()).WithRuntimeObjects(objects...).Build()
	builder := NewKubernetesContextBuilder(c)

	result, err := builder.BuildContext(context.Background(), platformv1alpha1.ResourceRef{
		Kind: "Deployment", Name: "big", Namespace: "default",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) > maxContextChars {
		t.Errorf("expected result to be truncated to %d chars, got %d", maxContextChars, len(result))
	}
}

func TestDiffContainerImages(t *testing.T) {
	current := []corev1.Container{
		{Name: "app", Image: "api:v2"},
		{Name: "sidecar", Image: "proxy:v1"},
	}
	previous := []corev1.Container{
		{Name: "app", Image: "api:v1"},
		{Name: "sidecar", Image: "proxy:v1"},
		{Name: "init", Image: "init:v1"},
	}

	diffs := diffContainerImages(current, previous)

	if len(diffs) != 2 {
		t.Fatalf("expected 2 diffs, got %d: %v", len(diffs), diffs)
	}

	foundImageChange := false
	foundRemoved := false
	for _, d := range diffs {
		if strings.Contains(d, "api:v1 → api:v2") {
			foundImageChange = true
		}
		if strings.Contains(d, "init") && strings.Contains(d, "removed") {
			foundRemoved = true
		}
	}

	if !foundImageChange {
		t.Error("expected image change diff for 'app'")
	}
	if !foundRemoved {
		t.Error("expected removed diff for 'init'")
	}
}

func TestIsPodOwnedByDeployment(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "ReplicaSet", Name: "myapp-abc12"},
			},
		},
	}

	if !isPodOwnedByDeployment(pod, "myapp") {
		t.Error("expected pod to be owned by deployment 'myapp'")
	}
	if isPodOwnedByDeployment(pod, "other") {
		t.Error("expected pod NOT to be owned by deployment 'other'")
	}
}
