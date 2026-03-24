/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
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
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"
)

// getLabelSelector returns the label selector string for any supported workload kind.
// For CronJobs, returns a prefix-based label match since CronJobs have no direct selector.
func getLabelSelector(ctx context.Context, clientset kubernetes.Interface, namespace, name, kind string) (string, error) {
	switch kind {
	case "Deployment":
		obj, err := clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("failed to get Deployment %s/%s: %w", namespace, name, err)
		}
		sel, err := metav1.LabelSelectorAsSelector(obj.Spec.Selector)
		if err != nil {
			return "", fmt.Errorf("failed to parse Deployment selector: %w", err)
		}
		return sel.String(), nil

	case "StatefulSet":
		obj, err := clientset.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("failed to get StatefulSet %s/%s: %w", namespace, name, err)
		}
		sel, err := metav1.LabelSelectorAsSelector(obj.Spec.Selector)
		if err != nil {
			return "", fmt.Errorf("failed to parse StatefulSet selector: %w", err)
		}
		return sel.String(), nil

	case "DaemonSet":
		obj, err := clientset.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("failed to get DaemonSet %s/%s: %w", namespace, name, err)
		}
		sel, err := metav1.LabelSelectorAsSelector(obj.Spec.Selector)
		if err != nil {
			return "", fmt.Errorf("failed to parse DaemonSet selector: %w", err)
		}
		return sel.String(), nil

	case "Job":
		obj, err := clientset.BatchV1().Jobs(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("failed to get Job %s/%s: %w", namespace, name, err)
		}
		if obj.Spec.Selector != nil {
			sel, err := metav1.LabelSelectorAsSelector(obj.Spec.Selector)
			if err != nil {
				return "", fmt.Errorf("failed to parse Job selector: %w", err)
			}
			return sel.String(), nil
		}
		// Fallback: use job-name label
		return labels.Set{"job-name": name}.String(), nil

	case "CronJob":
		// CronJobs don't have a direct selector; match pods via child Jobs
		return labels.Set{"job-name": name}.String(), nil

	default:
		return "", fmt.Errorf("unsupported resource kind: %s", kind)
	}
}

// collectResourceStatus collects the ResourceStatus for any supported workload kind.
func collectResourceStatus(ctx context.Context, clientset kubernetes.Interface, namespace, name, kind string) (ResourceStatus, error) {
	rs := ResourceStatus{Kind: kind, Name: name, Namespace: namespace}

	switch kind {
	case "Deployment":
		obj, err := clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return rs, fmt.Errorf("failed to get Deployment: %w", err)
		}
		if obj.Spec.Replicas != nil {
			rs.Replicas = *obj.Spec.Replicas
		}
		rs.ReadyReplicas = obj.Status.ReadyReplicas
		rs.UpdatedReplicas = obj.Status.UpdatedReplicas
		rs.AvailableReplicas = obj.Status.AvailableReplicas
		rs.UnavailableCount = obj.Status.UnavailableReplicas
		if obj.Spec.Strategy.Type != "" {
			rs.Strategy = string(obj.Spec.Strategy.Type)
		}
		for _, cond := range obj.Status.Conditions {
			rs.Conditions = append(rs.Conditions, fmt.Sprintf("%s=%s (%s)", cond.Type, cond.Status, cond.Reason))
		}

	case "StatefulSet":
		obj, err := clientset.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return rs, fmt.Errorf("failed to get StatefulSet: %w", err)
		}
		if obj.Spec.Replicas != nil {
			rs.Replicas = *obj.Spec.Replicas
		}
		rs.ReadyReplicas = obj.Status.ReadyReplicas
		rs.UpdatedReplicas = obj.Status.UpdatedReplicas
		rs.AvailableReplicas = obj.Status.CurrentReplicas
		rs.Strategy = string(obj.Spec.UpdateStrategy.Type)
		for _, cond := range obj.Status.Conditions {
			rs.Conditions = append(rs.Conditions, fmt.Sprintf("%s=%s (%s)", cond.Type, cond.Status, cond.Reason))
		}

	case "DaemonSet":
		obj, err := clientset.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return rs, fmt.Errorf("failed to get DaemonSet: %w", err)
		}
		rs.Replicas = obj.Status.DesiredNumberScheduled
		rs.ReadyReplicas = obj.Status.NumberReady
		rs.UpdatedReplicas = obj.Status.UpdatedNumberScheduled
		rs.AvailableReplicas = obj.Status.NumberAvailable
		rs.UnavailableCount = obj.Status.NumberUnavailable
		rs.Strategy = string(obj.Spec.UpdateStrategy.Type)
		for _, cond := range obj.Status.Conditions {
			rs.Conditions = append(rs.Conditions, fmt.Sprintf("%s=%s (%s)", cond.Type, cond.Status, cond.Reason))
		}

	case "Job":
		obj, err := clientset.BatchV1().Jobs(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return rs, fmt.Errorf("failed to get Job: %w", err)
		}
		if obj.Spec.Parallelism != nil {
			rs.Replicas = *obj.Spec.Parallelism
		}
		rs.Active = obj.Status.Active
		rs.Succeeded = obj.Status.Succeeded
		rs.Failed = obj.Status.Failed
		rs.ReadyReplicas = obj.Status.Active
		rs.AvailableReplicas = obj.Status.Succeeded
		rs.UnavailableCount = obj.Status.Failed
		if obj.Spec.Suspend != nil {
			rs.Suspended = *obj.Spec.Suspend
		}
		for _, cond := range obj.Status.Conditions {
			rs.Conditions = append(rs.Conditions, fmt.Sprintf("%s=%s (%s)", cond.Type, cond.Status, cond.Reason))
		}

	case "CronJob":
		obj, err := clientset.BatchV1().CronJobs(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return rs, fmt.Errorf("failed to get CronJob: %w", err)
		}
		rs.Schedule = obj.Spec.Schedule
		rs.Active = int32(len(obj.Status.Active))
		if obj.Spec.Suspend != nil {
			rs.Suspended = *obj.Spec.Suspend
		}
		if obj.Status.LastScheduleTime != nil {
			t := obj.Status.LastScheduleTime.Time
			rs.LastScheduleTime = &t
		}
		// CronJob doesn't have replica counts
	}

	return rs, nil
}

// Ensure imports are used
var _ = batchv1.Job{}

// DeploymentCollector collects resource status and pod information for any workload kind.
// Despite the name (kept for backward compatibility), it supports Deployment, StatefulSet,
// DaemonSet, Job, and CronJob via the kind field.
type DeploymentCollector struct {
	clientset  kubernetes.Interface
	namespace  string
	deployment string
	kind       string // resource kind (default: Deployment)
	logger     *zap.Logger
}

// NewDeploymentCollector creates a collector for deployment and pod status.
// For backward compatibility, kind defaults to "Deployment".
func NewDeploymentCollector(clientset kubernetes.Interface, namespace, deployment string, logger *zap.Logger) *DeploymentCollector {
	return &DeploymentCollector{
		clientset:  clientset,
		namespace:  namespace,
		deployment: deployment,
		kind:       "Deployment",
		logger:     logger,
	}
}

// NewResourceCollector creates a collector for any Kubernetes workload kind.
func NewResourceCollector(clientset kubernetes.Interface, namespace, name, kind string, logger *zap.Logger) *DeploymentCollector {
	if kind == "" {
		kind = "Deployment"
	}
	return &DeploymentCollector{
		clientset:  clientset,
		namespace:  namespace,
		deployment: name,
		kind:       kind,
		logger:     logger,
	}
}

// Collect gathers resource status and pod information for the configured workload kind.
func (c *DeploymentCollector) Collect(ctx context.Context) (*ResourceSnapshot, error) {
	snap := &ResourceSnapshot{
		Timestamp: time.Now(),
	}

	// Collect resource status (works for all kinds)
	rs, err := collectResourceStatus(ctx, c.clientset, c.namespace, c.deployment, c.kind)
	if err != nil {
		return nil, err
	}
	snap.Resource = rs
	snap.SyncDeploymentAlias() // backward compat

	// Get pods via label selector
	selectorStr, err := getLabelSelector(ctx, c.clientset, c.namespace, c.deployment, c.kind)
	if err != nil {
		c.logger.Warn("Failed to get label selector, falling back to name prefix",
			zap.String("kind", c.kind), zap.Error(err))
		// Fallback: list all pods and filter by name prefix
		selectorStr = ""
	}

	var podList *corev1.PodList
	if selectorStr != "" {
		podList, err = c.clientset.CoreV1().Pods(c.namespace).List(ctx, metav1.ListOptions{
			LabelSelector: selectorStr,
		})
	} else {
		podList, err = c.clientset.CoreV1().Pods(c.namespace).List(ctx, metav1.ListOptions{})
	}
	if err != nil {
		return nil, fmt.Errorf("failed to list pods: %w", err)
	}

	snap.Pods = make([]PodStatus, 0, len(podList.Items))
	for _, pod := range podList.Items {
		// If no selector was available, filter by name prefix
		if selectorStr == "" && !strings.HasPrefix(pod.Name, c.deployment) {
			continue
		}
		snap.Pods = append(snap.Pods, c.extractPodStatus(&pod))
	}

	return snap, nil
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

// EventCollector collects Kubernetes events related to a resource and its pods.
type EventCollector struct {
	clientset  kubernetes.Interface
	namespace  string
	deployment string
	kind       string
	logger     *zap.Logger
}

// NewEventCollector creates an event collector.
func NewEventCollector(clientset kubernetes.Interface, namespace, deployment string, logger *zap.Logger) *EventCollector {
	return &EventCollector{
		clientset:  clientset,
		namespace:  namespace,
		deployment: deployment,
		kind:       "Deployment",
		logger:     logger,
	}
}

// NewResourceEventCollector creates an event collector for any resource kind.
func NewResourceEventCollector(clientset kubernetes.Interface, namespace, name, kind string, logger *zap.Logger) *EventCollector {
	if kind == "" {
		kind = "Deployment"
	}
	return &EventCollector{clientset: clientset, namespace: namespace, deployment: name, kind: kind, logger: logger}
}

// Collect gathers recent events for the resource and its pods.
func (c *EventCollector) Collect(ctx context.Context) ([]K8sEvent, error) {
	// Get events for the resource itself
	events, err := c.clientset.CoreV1().Events(c.namespace).List(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("involvedObject.name=%s", c.deployment),
	})
	if err != nil {
		c.logger.Warn("Failed to list resource events", zap.Error(err))
	}

	// Get pods via label selector to find pod events
	selectorStr, err := getLabelSelector(ctx, c.clientset, c.namespace, c.deployment, c.kind)
	if err != nil {
		c.logger.Warn("Failed to get selector for events, using resource events only", zap.Error(err))
		result := make([]K8sEvent, 0)
		if events != nil {
			for _, ev := range events.Items {
				result = append(result, eventToK8sEvent(&ev))
			}
		}
		return result, nil
	}

	podList, err := c.clientset.CoreV1().Pods(c.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selectorStr,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods: %w", err)
	}

	podNames := make(map[string]bool)
	for _, pod := range podList.Items {
		podNames[pod.Name] = true
	}

	allEvents, err := c.clientset.CoreV1().Events(c.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list events: %w", err)
	}

	result := make([]K8sEvent, 0)
	if events != nil {
		for _, ev := range events.Items {
			result = append(result, eventToK8sEvent(&ev))
		}
	}
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
	kind       string
	maxLines   int
	logger     *zap.Logger
}

// NewLogCollector creates a log collector.
func NewLogCollector(clientset kubernetes.Interface, namespace, deployment string, maxLines int, logger *zap.Logger) *LogCollector {
	return &LogCollector{
		clientset:  clientset,
		namespace:  namespace,
		deployment: deployment,
		kind:       "Deployment",
		maxLines:   maxLines,
		logger:     logger,
	}
}

// NewResourceLogCollector creates a log collector for any resource kind.
func NewResourceLogCollector(clientset kubernetes.Interface, namespace, name, kind string, maxLines int, logger *zap.Logger) *LogCollector {
	if kind == "" {
		kind = "Deployment"
	}
	return &LogCollector{clientset: clientset, namespace: namespace, deployment: name, kind: kind, maxLines: maxLines, logger: logger}
}

// Collect gathers recent logs from all pods of the resource.
func (c *LogCollector) Collect(ctx context.Context) ([]LogEntry, error) {
	selectorStr, err := getLabelSelector(ctx, c.clientset, c.namespace, c.deployment, c.kind)
	if err != nil {
		return nil, fmt.Errorf("failed to get selector for %s/%s: %w", c.kind, c.deployment, err)
	}

	pods, err := c.clientset.CoreV1().Pods(c.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selectorStr,
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
	kind       string
	logger     *zap.Logger
}

// NewHPACollector creates an HPA collector.
func NewHPACollector(clientset kubernetes.Interface, namespace, deployment string, logger *zap.Logger) *HPACollector {
	return &HPACollector{
		clientset:  clientset,
		namespace:  namespace,
		deployment: deployment,
		kind:       "Deployment",
		logger:     logger,
	}
}

// NewResourceHPACollector creates an HPA collector for any resource kind.
func NewResourceHPACollector(clientset kubernetes.Interface, namespace, name, kind string, logger *zap.Logger) *HPACollector {
	if kind == "" {
		kind = "Deployment"
	}
	return &HPACollector{clientset: clientset, namespace: namespace, deployment: name, kind: kind, logger: logger}
}

// Collect gathers HPA status if one exists for the resource.
func (c *HPACollector) Collect(ctx context.Context) (*HPAStatus, error) {
	// Jobs and CronJobs cannot have HPA
	if c.kind == "Job" || c.kind == "CronJob" {
		return nil, nil
	}

	hpas, err := c.clientset.AutoscalingV2().HorizontalPodAutoscalers(c.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil // HPA might not be available, not an error
	}

	for _, hpa := range hpas.Items {
		if hpa.Spec.ScaleTargetRef.Kind == c.kind && hpa.Spec.ScaleTargetRef.Name == c.deployment {
			return c.extractHPAStatus(&hpa), nil
		}
	}

	return nil, nil // No HPA for this resource
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

// NodeCollector collects health status for nodes where target pods run.
type NodeCollector struct {
	clientset     kubernetes.Interface
	metricsClient metricsv.Interface
	namespace     string
	name          string
	kind          string
	logger        *zap.Logger
}

// NewNodeCollector creates a NodeCollector.
func NewNodeCollector(clientset kubernetes.Interface, metricsClient metricsv.Interface, namespace, name, kind string, logger *zap.Logger) *NodeCollector {
	return &NodeCollector{
		clientset:     clientset,
		metricsClient: metricsClient,
		namespace:     namespace,
		name:          name,
		kind:          kind,
		logger:        logger,
	}
}

// Collect returns the health status of all nodes where the target's pods are running.
func (nc *NodeCollector) Collect(ctx context.Context, pods []PodStatus) []NodeStatus {
	// Find unique node names from pod spec
	nodeNames := nc.getNodeNames(ctx, pods)
	if len(nodeNames) == 0 {
		return nil
	}

	// Collect node metrics (optional)
	nodeMetrics := make(map[string]struct{ cpu, mem string })
	if nc.metricsClient != nil {
		nmList, err := nc.metricsClient.MetricsV1beta1().NodeMetricses().List(ctx, metav1.ListOptions{})
		if err == nil {
			for _, nm := range nmList.Items {
				if _, relevant := nodeNames[nm.Name]; relevant {
					nodeMetrics[nm.Name] = struct{ cpu, mem string }{
						cpu: nm.Usage.Cpu().String(),
						mem: nm.Usage.Memory().String(),
					}
				}
			}
		}
	}

	// Fetch node objects
	var result []NodeStatus
	for nodeName := range nodeNames {
		node, err := nc.clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		if err != nil {
			nc.logger.Debug("Failed to get node", zap.String("node", nodeName), zap.Error(err))
			continue
		}

		ns := NodeStatus{
			Name:              node.Name,
			Unschedulable:     node.Spec.Unschedulable,
			KubeletVersion:    node.Status.NodeInfo.KubeletVersion,
			CPUCapacity:       node.Status.Capacity.Cpu().String(),
			MemoryCapacity:    node.Status.Capacity.Memory().String(),
			CPUAllocatable:    node.Status.Allocatable.Cpu().String(),
			MemoryAllocatable: node.Status.Allocatable.Memory().String(),
			PodCapacity:       int32(node.Status.Capacity.Pods().Value()),
		}

		// Parse conditions
		for _, cond := range node.Status.Conditions {
			switch cond.Type {
			case corev1.NodeReady:
				ns.Ready = cond.Status == corev1.ConditionTrue
				if cond.Status != corev1.ConditionTrue {
					ns.Conditions = append(ns.Conditions, fmt.Sprintf("NotReady: %s", cond.Message))
				}
			case corev1.NodeDiskPressure:
				ns.DiskPressure = cond.Status == corev1.ConditionTrue
				if cond.Status == corev1.ConditionTrue {
					ns.Conditions = append(ns.Conditions, fmt.Sprintf("DiskPressure: %s", cond.Message))
				}
			case corev1.NodeMemoryPressure:
				ns.MemoryPressure = cond.Status == corev1.ConditionTrue
				if cond.Status == corev1.ConditionTrue {
					ns.Conditions = append(ns.Conditions, fmt.Sprintf("MemoryPressure: %s", cond.Message))
				}
			case corev1.NodePIDPressure:
				ns.PIDPressure = cond.Status == corev1.ConditionTrue
				if cond.Status == corev1.ConditionTrue {
					ns.Conditions = append(ns.Conditions, fmt.Sprintf("PIDPressure: %s", cond.Message))
				}
			case corev1.NodeNetworkUnavailable:
				ns.NetworkUnavail = cond.Status == corev1.ConditionTrue
				if cond.Status == corev1.ConditionTrue {
					ns.Conditions = append(ns.Conditions, fmt.Sprintf("NetworkUnavailable: %s", cond.Message))
				}
			}
		}

		// Enrich with metrics
		if m, ok := nodeMetrics[node.Name]; ok {
			ns.CPUUsage = m.cpu
			ns.MemoryUsage = m.mem
		}

		// Count pods on this node
		podList, err := nc.clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{
			FieldSelector: "spec.nodeName=" + node.Name + ",status.phase!=Succeeded,status.phase!=Failed",
		})
		if err == nil {
			ns.PodCount = int32(len(podList.Items))
		}

		result = append(result, ns)
	}

	return result
}

// getNodeNames returns a set of node names where the target's pods are running.
func (nc *NodeCollector) getNodeNames(ctx context.Context, pods []PodStatus) map[string]struct{} {
	sel, err := getLabelSelector(ctx, nc.clientset, nc.namespace, nc.name, nc.kind)
	if err != nil {
		nc.logger.Debug("Failed to get label selector for node discovery", zap.Error(err))
		return nil
	}

	podList, err := nc.clientset.CoreV1().Pods(nc.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: sel,
	})
	if err != nil {
		nc.logger.Debug("Failed to list pods for node discovery", zap.Error(err))
		return nil
	}

	nodes := make(map[string]struct{})
	for _, pod := range podList.Items {
		if pod.Spec.NodeName != "" {
			nodes[pod.Spec.NodeName] = struct{}{}
		}
	}
	return nodes
}
