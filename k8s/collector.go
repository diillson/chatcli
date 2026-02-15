/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package k8s

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"
)

// DeploymentCollector collects deployment status and pod information.
type DeploymentCollector struct {
	clientset  kubernetes.Interface
	namespace  string
	deployment string
	logger     *zap.Logger
}

// NewDeploymentCollector creates a collector for deployment and pod status.
func NewDeploymentCollector(clientset kubernetes.Interface, namespace, deployment string, logger *zap.Logger) *DeploymentCollector {
	return &DeploymentCollector{
		clientset:  clientset,
		namespace:  namespace,
		deployment: deployment,
		logger:     logger,
	}
}

// Collect gathers deployment status and pod information.
func (c *DeploymentCollector) Collect(ctx context.Context) (*ResourceSnapshot, error) {
	snap := &ResourceSnapshot{
		Timestamp: time.Now(),
	}

	// Get deployment
	deploy, err := c.clientset.AppsV1().Deployments(c.namespace).Get(ctx, c.deployment, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get deployment %s/%s: %w", c.namespace, c.deployment, err)
	}

	snap.Deployment = c.extractDeploymentStatus(deploy)

	// Get pods for this deployment via label selector
	selector, err := metav1.LabelSelectorAsSelector(deploy.Spec.Selector)
	if err != nil {
		return nil, fmt.Errorf("failed to parse deployment selector: %w", err)
	}

	pods, err := c.clientset.CoreV1().Pods(c.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector.String(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods: %w", err)
	}

	snap.Pods = make([]PodStatus, 0, len(pods.Items))
	for _, pod := range pods.Items {
		snap.Pods = append(snap.Pods, c.extractPodStatus(&pod))
	}

	return snap, nil
}

func (c *DeploymentCollector) extractDeploymentStatus(deploy *appsv1.Deployment) DeploymentStatus {
	status := DeploymentStatus{
		Name:              deploy.Name,
		Namespace:         deploy.Namespace,
		Replicas:          *deploy.Spec.Replicas,
		ReadyReplicas:     deploy.Status.ReadyReplicas,
		UpdatedReplicas:   deploy.Status.UpdatedReplicas,
		AvailableReplicas: deploy.Status.AvailableReplicas,
	}

	if deploy.Spec.Strategy.Type != "" {
		status.Strategy = string(deploy.Spec.Strategy.Type)
	}

	for _, cond := range deploy.Status.Conditions {
		status.Conditions = append(status.Conditions,
			fmt.Sprintf("%s=%s (%s)", cond.Type, cond.Status, cond.Reason))
	}

	return status
}

func (c *DeploymentCollector) extractPodStatus(pod *corev1.Pod) PodStatus {
	ps := PodStatus{
		Name:           pod.Name,
		Phase:          string(pod.Status.Phase),
		ContainerCount: len(pod.Spec.Containers),
	}

	if pod.Status.StartTime != nil {
		t := pod.Status.StartTime.Time
		ps.StartTime = &t
	}

	for _, cs := range pod.Status.ContainerStatuses {
		ps.RestartCount += cs.RestartCount
		if cs.Ready {
			ps.ReadyCount++
		}

		// Check for OOMKilled or CrashLoopBackOff
		if cs.LastTerminationState.Terminated != nil {
			term := cs.LastTerminationState.Terminated
			ps.LastTerminated = &TerminationInfo{
				Reason:    term.Reason,
				ExitCode:  term.ExitCode,
				StartedAt: term.StartedAt.Time,
				EndedAt:   term.FinishedAt.Time,
			}
		}
	}

	ps.Ready = ps.ReadyCount == ps.ContainerCount

	for _, cond := range pod.Status.Conditions {
		if cond.Status == corev1.ConditionFalse {
			ps.Conditions = append(ps.Conditions,
				fmt.Sprintf("%s=%s (%s: %s)", cond.Type, cond.Status, cond.Reason, cond.Message))
		}
	}

	return ps
}

// EventCollector collects Kubernetes events related to the deployment.
type EventCollector struct {
	clientset  kubernetes.Interface
	namespace  string
	deployment string
	logger     *zap.Logger
}

// NewEventCollector creates an event collector.
func NewEventCollector(clientset kubernetes.Interface, namespace, deployment string, logger *zap.Logger) *EventCollector {
	return &EventCollector{
		clientset:  clientset,
		namespace:  namespace,
		deployment: deployment,
		logger:     logger,
	}
}

// Collect gathers recent events for the deployment and its pods.
func (c *EventCollector) Collect(ctx context.Context) ([]K8sEvent, error) {
	events, err := c.clientset.CoreV1().Events(c.namespace).List(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("involvedObject.name=%s", c.deployment),
	})
	if err != nil {
		c.logger.Warn("Failed to list deployment events", zap.Error(err))
	}

	// Also get events for pods matching the deployment
	deploy, err := c.clientset.AppsV1().Deployments(c.namespace).Get(ctx, c.deployment, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get deployment: %w", err)
	}

	selector, err := metav1.LabelSelectorAsSelector(deploy.Spec.Selector)
	if err != nil {
		return nil, fmt.Errorf("failed to parse selector: %w", err)
	}

	podList, err := c.clientset.CoreV1().Pods(c.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector.String(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods: %w", err)
	}

	// Collect pod names for event filtering
	podNames := make(map[string]bool)
	for _, pod := range podList.Items {
		podNames[pod.Name] = true
	}

	// Get all namespace events and filter
	allEvents, err := c.clientset.CoreV1().Events(c.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list events: %w", err)
	}

	result := make([]K8sEvent, 0)

	// Add deployment events
	if events != nil {
		for _, ev := range events.Items {
			result = append(result, eventToK8sEvent(&ev))
		}
	}

	// Add pod events
	for _, ev := range allEvents.Items {
		if podNames[ev.InvolvedObject.Name] {
			result = append(result, eventToK8sEvent(&ev))
		}
	}

	return result, nil
}

func eventToK8sEvent(ev *corev1.Event) K8sEvent {
	ts := ev.LastTimestamp.Time
	if ts.IsZero() {
		ts = ev.CreationTimestamp.Time
	}
	return K8sEvent{
		Timestamp: ts,
		Type:      ev.Type,
		Reason:    ev.Reason,
		Message:   ev.Message,
		Object:    fmt.Sprintf("%s/%s", ev.InvolvedObject.Kind, ev.InvolvedObject.Name),
		Count:     ev.Count,
	}
}

// LogCollector collects recent logs from pods.
type LogCollector struct {
	clientset  kubernetes.Interface
	namespace  string
	deployment string
	maxLines   int
	logger     *zap.Logger
}

// NewLogCollector creates a log collector.
func NewLogCollector(clientset kubernetes.Interface, namespace, deployment string, maxLines int, logger *zap.Logger) *LogCollector {
	return &LogCollector{
		clientset:  clientset,
		namespace:  namespace,
		deployment: deployment,
		maxLines:   maxLines,
		logger:     logger,
	}
}

// Collect gathers recent logs from all pods of the deployment.
func (c *LogCollector) Collect(ctx context.Context) ([]LogEntry, error) {
	deploy, err := c.clientset.AppsV1().Deployments(c.namespace).Get(ctx, c.deployment, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get deployment: %w", err)
	}

	selector, err := metav1.LabelSelectorAsSelector(deploy.Spec.Selector)
	if err != nil {
		return nil, fmt.Errorf("failed to parse selector: %w", err)
	}

	pods, err := c.clientset.CoreV1().Pods(c.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector.String(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods: %w", err)
	}

	tailLines := int64(c.maxLines)
	result := make([]LogEntry, 0)

	for _, pod := range pods.Items {
		for _, container := range pod.Spec.Containers {
			req := c.clientset.CoreV1().Pods(c.namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
				Container:  container.Name,
				TailLines:  &tailLines,
				Timestamps: true,
			})

			stream, err := req.Stream(ctx)
			if err != nil {
				c.logger.Debug("Failed to get logs for pod/container",
					zap.String("pod", pod.Name),
					zap.String("container", container.Name),
					zap.Error(err))
				continue
			}

			entries := c.parseLogs(stream, pod.Name, container.Name)
			stream.Close()
			result = append(result, entries...)
		}
	}

	return result, nil
}

func (c *LogCollector) parseLogs(reader io.Reader, podName, container string) []LogEntry {
	entries := make([]LogEntry, 0)
	scanner := bufio.NewScanner(reader)

	for scanner.Scan() {
		line := scanner.Text()
		entry := LogEntry{
			PodName:   podName,
			Container: container,
			Line:      line,
			Timestamp: time.Now(),
		}

		// Try to parse timestamp from log line (format: 2024-01-01T00:00:00.000Z log line)
		if len(line) > 30 && (line[4] == '-' || line[10] == 'T') {
			if ts, err := time.Parse(time.RFC3339Nano, line[:strings.IndexByte(line, ' ')]); err == nil {
				entry.Timestamp = ts
				entry.Line = line[strings.IndexByte(line, ' ')+1:]
			}
		}

		// Detect error-level log lines
		lower := strings.ToLower(entry.Line)
		entry.IsError = strings.Contains(lower, "error") ||
			strings.Contains(lower, "fatal") ||
			strings.Contains(lower, "panic") ||
			strings.Contains(lower, "exception") ||
			strings.Contains(lower, "oomkilled")

		entries = append(entries, entry)
	}

	return entries
}

// HPACollector collects HorizontalPodAutoscaler status.
type HPACollector struct {
	clientset  kubernetes.Interface
	namespace  string
	deployment string
	logger     *zap.Logger
}

// NewHPACollector creates an HPA collector.
func NewHPACollector(clientset kubernetes.Interface, namespace, deployment string, logger *zap.Logger) *HPACollector {
	return &HPACollector{
		clientset:  clientset,
		namespace:  namespace,
		deployment: deployment,
		logger:     logger,
	}
}

// Collect gathers HPA status if one exists for the deployment.
func (c *HPACollector) Collect(ctx context.Context) (*HPAStatus, error) {
	hpas, err := c.clientset.AutoscalingV2().HorizontalPodAutoscalers(c.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil // HPA might not be available, not an error
	}

	for _, hpa := range hpas.Items {
		if hpa.Spec.ScaleTargetRef.Kind == "Deployment" && hpa.Spec.ScaleTargetRef.Name == c.deployment {
			return c.extractHPAStatus(&hpa), nil
		}
	}

	return nil, nil // No HPA for this deployment
}

func (c *HPACollector) extractHPAStatus(hpa *autoscalingv2.HorizontalPodAutoscaler) *HPAStatus {
	status := &HPAStatus{
		Name:            hpa.Name,
		MinReplicas:     *hpa.Spec.MinReplicas,
		MaxReplicas:     hpa.Spec.MaxReplicas,
		CurrentReplicas: hpa.Status.CurrentReplicas,
		DesiredReplicas: hpa.Status.DesiredReplicas,
	}

	for _, metric := range hpa.Status.CurrentMetrics {
		switch metric.Type {
		case autoscalingv2.ResourceMetricSourceType:
			if metric.Resource != nil {
				if metric.Resource.Current.AverageUtilization != nil {
					status.CurrentMetrics = append(status.CurrentMetrics,
						fmt.Sprintf("%s: current=%d%%", metric.Resource.Name, *metric.Resource.Current.AverageUtilization))
				} else if metric.Resource.Current.AverageValue != nil {
					status.CurrentMetrics = append(status.CurrentMetrics,
						fmt.Sprintf("%s: current=%s", metric.Resource.Name, metric.Resource.Current.AverageValue.String()))
				}
			}
		}
	}

	return status
}

// MetricsCollector collects resource usage metrics from metrics-server.
type MetricsCollector struct {
	metricsClient metricsv.Interface
	namespace     string
	deployment    string
	clientset     kubernetes.Interface
	logger        *zap.Logger
}

// NewMetricsCollector creates a metrics collector.
func NewMetricsCollector(clientset kubernetes.Interface, metricsClient metricsv.Interface, namespace, deployment string, logger *zap.Logger) *MetricsCollector {
	return &MetricsCollector{
		metricsClient: metricsClient,
		namespace:     namespace,
		deployment:    deployment,
		clientset:     clientset,
		logger:        logger,
	}
}

// Collect gathers CPU/memory usage for deployment pods.
func (c *MetricsCollector) Collect(ctx context.Context, pods []PodStatus) []PodStatus {
	if c.metricsClient == nil {
		return pods
	}

	podMetrics, err := c.metricsClient.MetricsV1beta1().PodMetricses(c.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		c.logger.Debug("Metrics server not available", zap.Error(err))
		return pods
	}

	// Build lookup map
	metricsMap := make(map[string]struct{ cpu, mem string })
	for _, pm := range podMetrics.Items {
		var totalCPU, totalMem int64
		for _, c := range pm.Containers {
			totalCPU += c.Usage.Cpu().MilliValue()
			totalMem += c.Usage.Memory().Value() / (1024 * 1024) // to Mi
		}
		metricsMap[pm.Name] = struct{ cpu, mem string }{
			cpu: fmt.Sprintf("%dm", totalCPU),
			mem: fmt.Sprintf("%dMi", totalMem),
		}
	}

	// Enrich pods with metrics
	for i := range pods {
		if m, ok := metricsMap[pods[i].Name]; ok {
			pods[i].CPUUsage = m.cpu
			pods[i].MemoryUsage = m.mem
		}
	}

	return pods
}
