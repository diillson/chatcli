package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
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
	case "Job":
		return re.captureJobSnapshot(ctx, resource, snapshot)
	case "CronJob":
		return re.captureCronJobSnapshot(ctx, resource, snapshot)
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

	// Capture update strategy
	snapshot.UpdateStrategyType = string(sts.Spec.UpdateStrategy.Type)
	if sts.Spec.UpdateStrategy.RollingUpdate != nil && sts.Spec.UpdateStrategy.RollingUpdate.Partition != nil {
		snapshot.Partition = sts.Spec.UpdateStrategy.RollingUpdate.Partition
	}

	// Capture restart annotation
	snapshot.Annotations = make(map[string]string)
	if sts.Spec.Template.Annotations != nil {
		if v, ok := sts.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"]; ok {
			snapshot.Annotations["kubectl.kubernetes.io/restartedAt"] = v
		}
	}

	// Capture HPA if one exists
	re.captureHPASnapshot(ctx, resource, snapshot)

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

	// Capture update strategy
	snapshot.UpdateStrategyType = string(ds.Spec.UpdateStrategy.Type)
	if ds.Spec.UpdateStrategy.RollingUpdate != nil && ds.Spec.UpdateStrategy.RollingUpdate.MaxUnavailable != nil {
		snapshot.MaxUnavailable = ds.Spec.UpdateStrategy.RollingUpdate.MaxUnavailable.String()
	}

	// Capture restart annotation
	snapshot.Annotations = make(map[string]string)
	if ds.Spec.Template.Annotations != nil {
		if v, ok := ds.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"]; ok {
			snapshot.Annotations["kubectl.kubernetes.io/restartedAt"] = v
		}
	}

	return snapshot, nil
}

func (re *RollbackEngine) captureJobSnapshot(ctx context.Context, resource platformv1alpha1.ResourceRef, snapshot *platformv1alpha1.ResourceSnapshot) (*platformv1alpha1.ResourceSnapshot, error) {
	var job batchv1.Job
	if err := re.client.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &job); err != nil {
		return nil, fmt.Errorf("capturing job snapshot: %w", err)
	}

	snapshot.Suspend = job.Spec.Suspend
	snapshot.ActiveDeadlineSeconds = job.Spec.ActiveDeadlineSeconds
	snapshot.BackoffLimit = job.Spec.BackoffLimit
	snapshot.Parallelism = job.Spec.Parallelism

	snapshot.ContainerImages = make(map[string]string)
	snapshot.ContainerResources = make(map[string]platformv1alpha1.ContainerResourceSnapshot)
	for _, c := range job.Spec.Template.Spec.Containers {
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

	// Serialize full spec for complex rollbacks
	if specJSON, err := json.Marshal(job.Spec); err == nil {
		snapshot.FullSpec = string(specJSON)
	}

	return snapshot, nil
}

func (re *RollbackEngine) captureCronJobSnapshot(ctx context.Context, resource platformv1alpha1.ResourceRef, snapshot *platformv1alpha1.ResourceSnapshot) (*platformv1alpha1.ResourceSnapshot, error) {
	var cj batchv1.CronJob
	if err := re.client.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &cj); err != nil {
		return nil, fmt.Errorf("capturing cronjob snapshot: %w", err)
	}

	snapshot.Suspend = cj.Spec.Suspend
	snapshot.Schedule = cj.Spec.Schedule
	snapshot.ConcurrencyPolicy = string(cj.Spec.ConcurrencyPolicy)
	snapshot.StartingDeadlineSeconds = cj.Spec.StartingDeadlineSeconds
	snapshot.SuccessfulJobsHistoryLimit = cj.Spec.SuccessfulJobsHistoryLimit
	snapshot.FailedJobsHistoryLimit = cj.Spec.FailedJobsHistoryLimit

	snapshot.ContainerImages = make(map[string]string)
	snapshot.ContainerResources = make(map[string]platformv1alpha1.ContainerResourceSnapshot)
	for _, c := range cj.Spec.JobTemplate.Spec.Template.Spec.Containers {
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

	// Serialize full spec for complex rollbacks
	if specJSON, err := json.Marshal(cj.Spec); err == nil {
		snapshot.FullSpec = string(specJSON)
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
	case "Job":
		return re.rollbackJob(ctx, snapshot)
	case "CronJob":
		return re.rollbackCronJob(ctx, snapshot)
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

	// Restore update strategy
	if snapshot.UpdateStrategyType != "" && string(sts.Spec.UpdateStrategy.Type) != snapshot.UpdateStrategyType {
		sts.Spec.UpdateStrategy.Type = appsv1.StatefulSetUpdateStrategyType(snapshot.UpdateStrategyType)
		changes = append(changes, fmt.Sprintf("updateStrategy: %s", snapshot.UpdateStrategyType))
	}
	if snapshot.Partition != nil {
		if sts.Spec.UpdateStrategy.RollingUpdate == nil {
			sts.Spec.UpdateStrategy.RollingUpdate = &appsv1.RollingUpdateStatefulSetStrategy{}
		}
		if sts.Spec.UpdateStrategy.RollingUpdate.Partition == nil || *sts.Spec.UpdateStrategy.RollingUpdate.Partition != *snapshot.Partition {
			sts.Spec.UpdateStrategy.RollingUpdate.Partition = snapshot.Partition
			changes = append(changes, fmt.Sprintf("partition: %d", *snapshot.Partition))
		}
	}

	if len(changes) == 0 {
		return "no changes to rollback (state unchanged)", nil
	}

	if err := re.client.Update(ctx, &sts); err != nil {
		return "", fmt.Errorf("applying statefulset rollback: %w", err)
	}

	// Rollback HPA if snapshot has data
	if snapshot.HPAMinReplicas != nil || snapshot.HPAMaxReplicas != nil {
		if hpaResult := re.rollbackHPA(ctx, snapshot); hpaResult != "" {
			changes = append(changes, hpaResult)
		}
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

	// Restore update strategy
	if snapshot.UpdateStrategyType != "" && string(ds.Spec.UpdateStrategy.Type) != snapshot.UpdateStrategyType {
		ds.Spec.UpdateStrategy.Type = appsv1.DaemonSetUpdateStrategyType(snapshot.UpdateStrategyType)
		changes = append(changes, fmt.Sprintf("updateStrategy: %s", snapshot.UpdateStrategyType))
	}
	if snapshot.MaxUnavailable != "" {
		mu := intstr.Parse(snapshot.MaxUnavailable)
		if ds.Spec.UpdateStrategy.RollingUpdate == nil {
			ds.Spec.UpdateStrategy.RollingUpdate = &appsv1.RollingUpdateDaemonSet{}
		}
		if ds.Spec.UpdateStrategy.RollingUpdate.MaxUnavailable == nil || ds.Spec.UpdateStrategy.RollingUpdate.MaxUnavailable.String() != snapshot.MaxUnavailable {
			ds.Spec.UpdateStrategy.RollingUpdate.MaxUnavailable = &mu
			changes = append(changes, fmt.Sprintf("maxUnavailable: %s", snapshot.MaxUnavailable))
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

func (re *RollbackEngine) rollbackJob(ctx context.Context, snapshot *platformv1alpha1.ResourceSnapshot) (string, error) {
	var job batchv1.Job
	if err := re.client.Get(ctx, types.NamespacedName{
		Name: snapshot.ResourceName, Namespace: snapshot.Namespace,
	}, &job); err != nil {
		return "", fmt.Errorf("getting job for rollback: %w", err)
	}

	var changes []string

	if snapshot.Suspend != nil && (job.Spec.Suspend == nil || *job.Spec.Suspend != *snapshot.Suspend) {
		job.Spec.Suspend = snapshot.Suspend
		changes = append(changes, fmt.Sprintf("suspend: %v", *snapshot.Suspend))
	}
	if snapshot.ActiveDeadlineSeconds != nil && (job.Spec.ActiveDeadlineSeconds == nil || *job.Spec.ActiveDeadlineSeconds != *snapshot.ActiveDeadlineSeconds) {
		job.Spec.ActiveDeadlineSeconds = snapshot.ActiveDeadlineSeconds
		changes = append(changes, fmt.Sprintf("activeDeadlineSeconds: %d", *snapshot.ActiveDeadlineSeconds))
	}
	if snapshot.BackoffLimit != nil && (job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != *snapshot.BackoffLimit) {
		job.Spec.BackoffLimit = snapshot.BackoffLimit
		changes = append(changes, fmt.Sprintf("backoffLimit: %d", *snapshot.BackoffLimit))
	}
	if snapshot.Parallelism != nil && (job.Spec.Parallelism == nil || *job.Spec.Parallelism != *snapshot.Parallelism) {
		job.Spec.Parallelism = snapshot.Parallelism
		changes = append(changes, fmt.Sprintf("parallelism: %d", *snapshot.Parallelism))
	}

	// Restore container resources
	for i := range job.Spec.Template.Spec.Containers {
		c := &job.Spec.Template.Spec.Containers[i]
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

	if err := re.client.Update(ctx, &job); err != nil {
		return "", fmt.Errorf("applying job rollback: %w", err)
	}

	return fmt.Sprintf("Rolled back Job %s/%s: %s", snapshot.Namespace, snapshot.ResourceName, strings.Join(changes, "; ")), nil
}

func (re *RollbackEngine) rollbackCronJob(ctx context.Context, snapshot *platformv1alpha1.ResourceSnapshot) (string, error) {
	var cj batchv1.CronJob
	if err := re.client.Get(ctx, types.NamespacedName{
		Name: snapshot.ResourceName, Namespace: snapshot.Namespace,
	}, &cj); err != nil {
		return "", fmt.Errorf("getting cronjob for rollback: %w", err)
	}

	var changes []string

	if snapshot.Suspend != nil && (cj.Spec.Suspend == nil || *cj.Spec.Suspend != *snapshot.Suspend) {
		cj.Spec.Suspend = snapshot.Suspend
		changes = append(changes, fmt.Sprintf("suspend: %v", *snapshot.Suspend))
	}
	if snapshot.Schedule != "" && cj.Spec.Schedule != snapshot.Schedule {
		cj.Spec.Schedule = snapshot.Schedule
		changes = append(changes, fmt.Sprintf("schedule: %s", snapshot.Schedule))
	}
	if snapshot.ConcurrencyPolicy != "" && string(cj.Spec.ConcurrencyPolicy) != snapshot.ConcurrencyPolicy {
		cj.Spec.ConcurrencyPolicy = batchv1.ConcurrencyPolicy(snapshot.ConcurrencyPolicy)
		changes = append(changes, fmt.Sprintf("concurrencyPolicy: %s", snapshot.ConcurrencyPolicy))
	}
	if snapshot.StartingDeadlineSeconds != nil && (cj.Spec.StartingDeadlineSeconds == nil || *cj.Spec.StartingDeadlineSeconds != *snapshot.StartingDeadlineSeconds) {
		cj.Spec.StartingDeadlineSeconds = snapshot.StartingDeadlineSeconds
		changes = append(changes, fmt.Sprintf("startingDeadlineSeconds: %d", *snapshot.StartingDeadlineSeconds))
	}
	if snapshot.SuccessfulJobsHistoryLimit != nil && (cj.Spec.SuccessfulJobsHistoryLimit == nil || *cj.Spec.SuccessfulJobsHistoryLimit != *snapshot.SuccessfulJobsHistoryLimit) {
		cj.Spec.SuccessfulJobsHistoryLimit = snapshot.SuccessfulJobsHistoryLimit
		changes = append(changes, fmt.Sprintf("successfulJobsHistoryLimit: %d", *snapshot.SuccessfulJobsHistoryLimit))
	}
	if snapshot.FailedJobsHistoryLimit != nil && (cj.Spec.FailedJobsHistoryLimit == nil || *cj.Spec.FailedJobsHistoryLimit != *snapshot.FailedJobsHistoryLimit) {
		cj.Spec.FailedJobsHistoryLimit = snapshot.FailedJobsHistoryLimit
		changes = append(changes, fmt.Sprintf("failedJobsHistoryLimit: %d", *snapshot.FailedJobsHistoryLimit))
	}

	// Restore container resources in jobTemplate
	for i := range cj.Spec.JobTemplate.Spec.Template.Spec.Containers {
		c := &cj.Spec.JobTemplate.Spec.Template.Spec.Containers[i]
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

	if err := re.client.Update(ctx, &cj); err != nil {
		return "", fmt.Errorf("applying cronjob rollback: %w", err)
	}

	return fmt.Sprintf("Rolled back CronJob %s/%s: %s", snapshot.Namespace, snapshot.ResourceName, strings.Join(changes, "; ")), nil
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

	case "DaemonSet":
		var ds appsv1.DaemonSet
		if err := re.client.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &ds); err != nil {
			return false
		}
		return ds.Status.NumberReady >= ds.Status.DesiredNumberScheduled && ds.Status.NumberUnavailable == 0

	case "Job":
		var job batchv1.Job
		if err := re.client.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &job); err != nil {
			return false
		}
		for _, cond := range job.Status.Conditions {
			if cond.Type == batchv1.JobComplete && cond.Status == "True" {
				return true
			}
		}
		return job.Status.Active > 0

	case "CronJob":
		var cj batchv1.CronJob
		if err := re.client.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &cj); err != nil {
			return false
		}
		// CronJob is healthy if suspended intentionally or has recent successful schedule
		if cj.Spec.Suspend != nil && *cj.Spec.Suspend {
			return true
		}
		return cj.Status.LastSuccessfulTime != nil || len(cj.Status.Active) > 0

	default:
		return false
	}
}
