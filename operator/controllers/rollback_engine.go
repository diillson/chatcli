package controllers

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// RollbackEngine captures resource state before remediation and restores it on failure.
type RollbackEngine struct {
	client client.Client
}

// NewRollbackEngine creates a new RollbackEngine.
func NewRollbackEngine(c client.Client) *RollbackEngine {
	return &RollbackEngine{client: c}
}

// CaptureSnapshot captures the full restorable state of a resource before remediation.
// Supports Deployment, StatefulSet, DaemonSet, and HPA.
func (re *RollbackEngine) CaptureSnapshot(ctx context.Context, resource platformv1alpha1.ResourceRef) (*platformv1alpha1.ResourceSnapshot, error) {
	snapshot := &platformv1alpha1.ResourceSnapshot{
		ResourceKind: resource.Kind,
		ResourceName: resource.Name,
		Namespace:    resource.Namespace,
		CapturedAt:   metav1.Now(),
	}

	switch resource.Kind {
	case "Deployment":
		return re.captureDeploymentSnapshot(ctx, resource, snapshot)
	case "StatefulSet":
		return re.captureStatefulSetSnapshot(ctx, resource, snapshot)
	case "DaemonSet":
		return re.captureDaemonSetSnapshot(ctx, resource, snapshot)
	default:
		// For unsupported kinds, capture what we can via pods
		return snapshot, nil
	}
}

func (re *RollbackEngine) captureDeploymentSnapshot(ctx context.Context, resource platformv1alpha1.ResourceRef, snapshot *platformv1alpha1.ResourceSnapshot) (*platformv1alpha1.ResourceSnapshot, error) {
	var deploy appsv1.Deployment
	if err := re.client.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &deploy); err != nil {
		return nil, fmt.Errorf("capturing deployment snapshot: %w", err)
	}

	if deploy.Spec.Replicas != nil {
		snapshot.Replicas = deploy.Spec.Replicas
	}

	snapshot.ContainerImages = make(map[string]string)
	snapshot.ContainerResources = make(map[string]platformv1alpha1.ContainerResourceSnapshot)

	for _, c := range deploy.Spec.Template.Spec.Containers {
		snapshot.ContainerImages[c.Name] = c.Image
		rs := platformv1alpha1.ContainerResourceSnapshot{}
		if c.Resources.Requests != nil {
			if cpu := c.Resources.Requests.Cpu(); cpu != nil {
				rs.CPURequest = cpu.String()
			}
			if mem := c.Resources.Requests.Memory(); mem != nil {
				rs.MemoryRequest = mem.String()
			}
		}
		if c.Resources.Limits != nil {
			if cpu := c.Resources.Limits.Cpu(); cpu != nil {
				rs.CPULimit = cpu.String()
			}
			if mem := c.Resources.Limits.Memory(); mem != nil {
				rs.MemoryLimit = mem.String()
			}
		}
		snapshot.ContainerResources[c.Name] = rs
	}

	// Capture restart annotation
	snapshot.Annotations = make(map[string]string)
	if deploy.Spec.Template.Annotations != nil {
		if v, ok := deploy.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"]; ok {
			snapshot.Annotations["kubectl.kubernetes.io/restartedAt"] = v
		}
	}

	// Also capture HPA if one exists
	re.captureHPASnapshot(ctx, resource, snapshot)

	return snapshot, nil
}

func (re *RollbackEngine) captureStatefulSetSnapshot(ctx context.Context, resource platformv1alpha1.ResourceRef, snapshot *platformv1alpha1.ResourceSnapshot) (*platformv1alpha1.ResourceSnapshot, error) {
	var sts appsv1.StatefulSet
	if err := re.client.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &sts); err != nil {
		return nil, fmt.Errorf("capturing statefulset snapshot: %w", err)
	}

	if sts.Spec.Replicas != nil {
		snapshot.Replicas = sts.Spec.Replicas
	}

	snapshot.ContainerImages = make(map[string]string)
	snapshot.ContainerResources = make(map[string]platformv1alpha1.ContainerResourceSnapshot)

	for _, c := range sts.Spec.Template.Spec.Containers {
		snapshot.ContainerImages[c.Name] = c.Image
		rs := platformv1alpha1.ContainerResourceSnapshot{}
		if c.Resources.Requests != nil {
			rs.CPURequest = c.Resources.Requests.Cpu().String()
			rs.MemoryRequest = c.Resources.Requests.Memory().String()
		}
		if c.Resources.Limits != nil {
			rs.CPULimit = c.Resources.Limits.Cpu().String()
			rs.MemoryLimit = c.Resources.Limits.Memory().String()
		}
		snapshot.ContainerResources[c.Name] = rs
	}

	return snapshot, nil
}

func (re *RollbackEngine) captureDaemonSetSnapshot(ctx context.Context, resource platformv1alpha1.ResourceRef, snapshot *platformv1alpha1.ResourceSnapshot) (*platformv1alpha1.ResourceSnapshot, error) {
	var ds appsv1.DaemonSet
	if err := re.client.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &ds); err != nil {
		return nil, fmt.Errorf("capturing daemonset snapshot: %w", err)
	}

	snapshot.ContainerImages = make(map[string]string)
	snapshot.ContainerResources = make(map[string]platformv1alpha1.ContainerResourceSnapshot)

	for _, c := range ds.Spec.Template.Spec.Containers {
		snapshot.ContainerImages[c.Name] = c.Image
		rs := platformv1alpha1.ContainerResourceSnapshot{}
		if c.Resources.Requests != nil {
			rs.CPURequest = c.Resources.Requests.Cpu().String()
			rs.MemoryRequest = c.Resources.Requests.Memory().String()
		}
		if c.Resources.Limits != nil {
			rs.CPULimit = c.Resources.Limits.Cpu().String()
			rs.MemoryLimit = c.Resources.Limits.Memory().String()
		}
		snapshot.ContainerResources[c.Name] = rs
	}

	return snapshot, nil
}

func (re *RollbackEngine) captureHPASnapshot(ctx context.Context, resource platformv1alpha1.ResourceRef, snapshot *platformv1alpha1.ResourceSnapshot) {
	var hpaList autoscalingv2.HorizontalPodAutoscalerList
	if err := re.client.List(ctx, &hpaList, client.InNamespace(resource.Namespace)); err != nil {
		return
	}
	for _, hpa := range hpaList.Items {
		if hpa.Spec.ScaleTargetRef.Name == resource.Name {
			if hpa.Spec.MinReplicas != nil {
				snapshot.HPAMinReplicas = hpa.Spec.MinReplicas
			}
			max := hpa.Spec.MaxReplicas
			snapshot.HPAMaxReplicas = &max
			break
		}
	}
}

// CaptureNodeSnapshot captures a node's schedulable state.
func (re *RollbackEngine) CaptureNodeSnapshot(ctx context.Context, nodeName string) (*platformv1alpha1.ResourceSnapshot, error) {
	var node corev1.Node
	if err := re.client.Get(ctx, types.NamespacedName{Name: nodeName}, &node); err != nil {
		return nil, fmt.Errorf("capturing node snapshot: %w", err)
	}
	unschedulable := node.Spec.Unschedulable
	return &platformv1alpha1.ResourceSnapshot{
		ResourceKind:      "Node",
		ResourceName:      nodeName,
		NodeUnschedulable: &unschedulable,
		CapturedAt:        metav1.Now(),
	}, nil
}

// Rollback restores a resource to its pre-remediation state from a snapshot.
// Returns a description of what was rolled back.
func (re *RollbackEngine) Rollback(ctx context.Context, snapshot *platformv1alpha1.ResourceSnapshot) (string, error) {
	logger := log.FromContext(ctx)

	if snapshot == nil {
		return "", fmt.Errorf("no snapshot available for rollback")
	}

	logger.Info("Performing automatic rollback",
		"kind", snapshot.ResourceKind, "name", snapshot.ResourceName,
		"namespace", snapshot.Namespace)

	switch snapshot.ResourceKind {
	case "Deployment":
		return re.rollbackDeployment(ctx, snapshot)
	case "StatefulSet":
		return re.rollbackStatefulSet(ctx, snapshot)
	case "DaemonSet":
		return re.rollbackDaemonSet(ctx, snapshot)
	case "Node":
		return re.rollbackNode(ctx, snapshot)
	default:
		return "", fmt.Errorf("rollback not supported for resource kind %s", snapshot.ResourceKind)
	}
}

func (re *RollbackEngine) rollbackDeployment(ctx context.Context, snapshot *platformv1alpha1.ResourceSnapshot) (string, error) {
	var deploy appsv1.Deployment
	if err := re.client.Get(ctx, types.NamespacedName{
		Name: snapshot.ResourceName, Namespace: snapshot.Namespace,
	}, &deploy); err != nil {
		return "", fmt.Errorf("getting deployment for rollback: %w", err)
	}

	var changes []string

	// Restore replicas
	if snapshot.Replicas != nil {
		current := int32(1)
		if deploy.Spec.Replicas != nil {
			current = *deploy.Spec.Replicas
		}
		if current != *snapshot.Replicas {
			deploy.Spec.Replicas = snapshot.Replicas
			changes = append(changes, fmt.Sprintf("replicas: %d → %d", current, *snapshot.Replicas))
		}
	}

	// Restore container resources
	for i := range deploy.Spec.Template.Spec.Containers {
		c := &deploy.Spec.Template.Spec.Containers[i]
		if rs, ok := snapshot.ContainerResources[c.Name]; ok {
			restored := re.restoreContainerResources(c, rs)
			if restored != "" {
				changes = append(changes, fmt.Sprintf("container %s: %s", c.Name, restored))
			}
		}
		// Restore image
		if img, ok := snapshot.ContainerImages[c.Name]; ok && c.Image != img {
			changes = append(changes, fmt.Sprintf("container %s image: %s → %s", c.Name, c.Image, img))
			c.Image = img
		}
	}

	if len(changes) == 0 {
		return "no changes to rollback (state unchanged)", nil
	}

	if err := re.client.Update(ctx, &deploy); err != nil {
		return "", fmt.Errorf("applying deployment rollback: %w", err)
	}

	// Also rollback HPA if snapshot has HPA data
	if snapshot.HPAMinReplicas != nil || snapshot.HPAMaxReplicas != nil {
		if hpaResult := re.rollbackHPA(ctx, snapshot); hpaResult != "" {
			changes = append(changes, hpaResult)
		}
	}

	result := fmt.Sprintf("Rolled back %s/%s: %s", snapshot.Namespace, snapshot.ResourceName, strings.Join(changes, "; "))
	return result, nil
}

func (re *RollbackEngine) rollbackStatefulSet(ctx context.Context, snapshot *platformv1alpha1.ResourceSnapshot) (string, error) {
	var sts appsv1.StatefulSet
	if err := re.client.Get(ctx, types.NamespacedName{
		Name: snapshot.ResourceName, Namespace: snapshot.Namespace,
	}, &sts); err != nil {
		return "", fmt.Errorf("getting statefulset for rollback: %w", err)
	}

	var changes []string

	if snapshot.Replicas != nil {
		current := int32(1)
		if sts.Spec.Replicas != nil {
			current = *sts.Spec.Replicas
		}
		if current != *snapshot.Replicas {
			sts.Spec.Replicas = snapshot.Replicas
			changes = append(changes, fmt.Sprintf("replicas: %d → %d", current, *snapshot.Replicas))
		}
	}

	for i := range sts.Spec.Template.Spec.Containers {
		c := &sts.Spec.Template.Spec.Containers[i]
		if rs, ok := snapshot.ContainerResources[c.Name]; ok {
			restored := re.restoreContainerResources(c, rs)
			if restored != "" {
				changes = append(changes, fmt.Sprintf("container %s: %s", c.Name, restored))
			}
		}
	}

	if len(changes) == 0 {
		return "no changes to rollback (state unchanged)", nil
	}

	if err := re.client.Update(ctx, &sts); err != nil {
		return "", fmt.Errorf("applying statefulset rollback: %w", err)
	}

	return fmt.Sprintf("Rolled back StatefulSet %s/%s: %s", snapshot.Namespace, snapshot.ResourceName, strings.Join(changes, "; ")), nil
}

func (re *RollbackEngine) rollbackDaemonSet(ctx context.Context, snapshot *platformv1alpha1.ResourceSnapshot) (string, error) {
	var ds appsv1.DaemonSet
	if err := re.client.Get(ctx, types.NamespacedName{
		Name: snapshot.ResourceName, Namespace: snapshot.Namespace,
	}, &ds); err != nil {
		return "", fmt.Errorf("getting daemonset for rollback: %w", err)
	}

	var changes []string
	for i := range ds.Spec.Template.Spec.Containers {
		c := &ds.Spec.Template.Spec.Containers[i]
		if rs, ok := snapshot.ContainerResources[c.Name]; ok {
			restored := re.restoreContainerResources(c, rs)
			if restored != "" {
				changes = append(changes, fmt.Sprintf("container %s: %s", c.Name, restored))
			}
		}
	}

	if len(changes) == 0 {
		return "no changes to rollback (state unchanged)", nil
	}

	if err := re.client.Update(ctx, &ds); err != nil {
		return "", fmt.Errorf("applying daemonset rollback: %w", err)
	}

	return fmt.Sprintf("Rolled back DaemonSet %s/%s: %s", snapshot.Namespace, snapshot.ResourceName, strings.Join(changes, "; ")), nil
}

func (re *RollbackEngine) rollbackNode(ctx context.Context, snapshot *platformv1alpha1.ResourceSnapshot) (string, error) {
	if snapshot.NodeUnschedulable == nil {
		return "no node state to rollback", nil
	}

	var node corev1.Node
	if err := re.client.Get(ctx, types.NamespacedName{Name: snapshot.ResourceName}, &node); err != nil {
		return "", fmt.Errorf("getting node for rollback: %w", err)
	}

	if node.Spec.Unschedulable == *snapshot.NodeUnschedulable {
		return "node state unchanged", nil
	}

	node.Spec.Unschedulable = *snapshot.NodeUnschedulable
	if err := re.client.Update(ctx, &node); err != nil {
		return "", fmt.Errorf("applying node rollback: %w", err)
	}

	return fmt.Sprintf("Rolled back node %s: unschedulable=%v", snapshot.ResourceName, *snapshot.NodeUnschedulable), nil
}

func (re *RollbackEngine) rollbackHPA(ctx context.Context, snapshot *platformv1alpha1.ResourceSnapshot) string {
	var hpaList autoscalingv2.HorizontalPodAutoscalerList
	if err := re.client.List(ctx, &hpaList, client.InNamespace(snapshot.Namespace)); err != nil {
		return ""
	}

	for i := range hpaList.Items {
		hpa := &hpaList.Items[i]
		if hpa.Spec.ScaleTargetRef.Name != snapshot.ResourceName {
			continue
		}

		var changes []string
		if snapshot.HPAMinReplicas != nil && (hpa.Spec.MinReplicas == nil || *hpa.Spec.MinReplicas != *snapshot.HPAMinReplicas) {
			hpa.Spec.MinReplicas = snapshot.HPAMinReplicas
			changes = append(changes, fmt.Sprintf("minReplicas=%d", *snapshot.HPAMinReplicas))
		}
		if snapshot.HPAMaxReplicas != nil && hpa.Spec.MaxReplicas != *snapshot.HPAMaxReplicas {
			hpa.Spec.MaxReplicas = *snapshot.HPAMaxReplicas
			changes = append(changes, fmt.Sprintf("maxReplicas=%d", *snapshot.HPAMaxReplicas))
		}

		if len(changes) > 0 {
			if err := re.client.Update(ctx, hpa); err != nil {
				return fmt.Sprintf("HPA rollback failed: %v", err)
			}
			return fmt.Sprintf("HPA: %s", strings.Join(changes, ", "))
		}
		break
	}
	return ""
}

// restoreContainerResources restores a container's CPU/memory to snapshot values.
func (re *RollbackEngine) restoreContainerResources(c *corev1.Container, rs platformv1alpha1.ContainerResourceSnapshot) string {
	var changes []string

	if c.Resources.Requests == nil {
		c.Resources.Requests = corev1.ResourceList{}
	}
	if c.Resources.Limits == nil {
		c.Resources.Limits = corev1.ResourceList{}
	}

	if rs.CPURequest != "" {
		qty := resource.MustParse(rs.CPURequest)
		if c.Resources.Requests.Cpu().Cmp(qty) != 0 {
			c.Resources.Requests[corev1.ResourceCPU] = qty
			changes = append(changes, fmt.Sprintf("cpu_request=%s", rs.CPURequest))
		}
	}
	if rs.MemoryRequest != "" {
		qty := resource.MustParse(rs.MemoryRequest)
		if c.Resources.Requests.Memory().Cmp(qty) != 0 {
			c.Resources.Requests[corev1.ResourceMemory] = qty
			changes = append(changes, fmt.Sprintf("memory_request=%s", rs.MemoryRequest))
		}
	}
	if rs.CPULimit != "" {
		qty := resource.MustParse(rs.CPULimit)
		if c.Resources.Limits.Cpu().Cmp(qty) != 0 {
			c.Resources.Limits[corev1.ResourceCPU] = qty
			changes = append(changes, fmt.Sprintf("cpu_limit=%s", rs.CPULimit))
		}
	}
	if rs.MemoryLimit != "" {
		qty := resource.MustParse(rs.MemoryLimit)
		if c.Resources.Limits.Memory().Cmp(qty) != 0 {
			c.Resources.Limits[corev1.ResourceMemory] = qty
			changes = append(changes, fmt.Sprintf("memory_limit=%s", rs.MemoryLimit))
		}
	}

	return strings.Join(changes, ", ")
}

// VerifyPostFailureHealth checks if the resource is healthy after a failed remediation.
func (re *RollbackEngine) VerifyPostFailureHealth(ctx context.Context, resource platformv1alpha1.ResourceRef) bool {
	switch resource.Kind {
	case "Deployment":
		var deploy appsv1.Deployment
		if err := re.client.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &deploy); err != nil {
			return false
		}
		desired := int32(1)
		if deploy.Spec.Replicas != nil {
			desired = *deploy.Spec.Replicas
		}
		return deploy.Status.ReadyReplicas >= desired && deploy.Status.UnavailableReplicas == 0

	case "StatefulSet":
		var sts appsv1.StatefulSet
		if err := re.client.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &sts); err != nil {
			return false
		}
		desired := int32(1)
		if sts.Spec.Replicas != nil {
			desired = *sts.Spec.Replicas
		}
		return sts.Status.ReadyReplicas >= desired

	default:
		return false
	}
}
