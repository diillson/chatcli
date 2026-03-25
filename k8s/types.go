/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package k8s

import "time"

// ResourceSnapshot captures the state of monitored resources at a point in time.
type ResourceSnapshot struct {
	Timestamp  time.Time
	Resource   ResourceStatus
	Deployment DeploymentStatus // Deprecated: alias for Resource (kept for backward compat)
	Pods       []PodStatus
	Nodes      []NodeStatus // nodes where the target's pods are running
	Events     []K8sEvent
	HPA        *HPAStatus
	AppMetrics *AppMetrics // application-level Prometheus metrics (nil if not configured)
}

// SyncDeploymentAlias copies Resource fields to the deprecated Deployment alias for backward compat.
func (s *ResourceSnapshot) SyncDeploymentAlias() {
	s.Deployment = DeploymentStatus{
		Name:              s.Resource.Name,
		Namespace:         s.Resource.Namespace,
		Replicas:          s.Resource.Replicas,
		ReadyReplicas:     s.Resource.ReadyReplicas,
		UpdatedReplicas:   s.Resource.UpdatedReplicas,
		AvailableReplicas: s.Resource.AvailableReplicas,
		Conditions:        s.Resource.Conditions,
		Strategy:          s.Resource.Strategy,
	}
}

// ResourceStatus holds resource-level information for any Kubernetes workload kind.
type ResourceStatus struct {
	Kind              string // Deployment, StatefulSet, DaemonSet, Job, CronJob
	Name              string
	Namespace         string
	Replicas          int32    // Desired replicas (Deployment/StatefulSet) or DesiredNumberScheduled (DaemonSet) or Parallelism (Job)
	ReadyReplicas     int32    // ReadyReplicas (Deploy/STS) or NumberReady (DS) or Active (Job)
	UpdatedReplicas   int32    // UpdatedReplicas (Deploy) or UpdatedReplicas (STS) or UpdatedNumberScheduled (DS)
	AvailableReplicas int32    // AvailableReplicas (Deploy) or NumberAvailable (DS) or Succeeded (Job)
	UnavailableCount  int32    // UnavailableReplicas (Deploy) or NumberUnavailable (DS) or Failed (Job)
	Conditions        []string // human-readable condition summaries
	Strategy          string   // Deployment strategy or StatefulSet/DaemonSet update strategy
	// Job/CronJob specific
	Succeeded        int32      // Job: succeeded count
	Failed           int32      // Job: failed count
	Active           int32      // Job: active count; CronJob: active jobs count
	Schedule         string     // CronJob: schedule expression
	Suspended        bool       // Job/CronJob: suspend state
	LastScheduleTime *time.Time // CronJob: last schedule
}

// DeploymentStatus holds deployment-level information.
// Deprecated: Use ResourceStatus instead. Kept for backward compatibility with existing code.
type DeploymentStatus struct {
	Name              string
	Namespace         string
	Replicas          int32
	ReadyReplicas     int32
	UpdatedReplicas   int32
	AvailableReplicas int32
	Conditions        []string // human-readable condition summaries
	Strategy          string
}

// PodStatus holds pod-level information.
type PodStatus struct {
	Name           string
	Phase          string // Running, Pending, Failed, etc.
	Ready          bool
	RestartCount   int32
	ContainerCount int
	ReadyCount     int
	StartTime      *time.Time
	Conditions     []string
	CPUUsage       string // e.g., "45m" (millicores)
	MemoryUsage    string // e.g., "120Mi"
	LastTerminated *TerminationInfo
}

// TerminationInfo holds info about the last container termination.
type TerminationInfo struct {
	Reason    string
	ExitCode  int32
	StartedAt time.Time
	EndedAt   time.Time
}

// K8sEvent represents a Kubernetes event related to the monitored resources.
type K8sEvent struct {
	Timestamp time.Time
	Type      string // Normal, Warning
	Reason    string
	Message   string
	Object    string // e.g., "Pod/myapp-xyz"
	Count     int32
}

// HPAStatus holds HorizontalPodAutoscaler information.
type HPAStatus struct {
	Name            string
	MinReplicas     int32
	MaxReplicas     int32
	CurrentReplicas int32
	DesiredReplicas int32
	CurrentMetrics  []string // human-readable metric summaries
}

// NodeStatus holds node-level health information.
type NodeStatus struct {
	Name              string
	Ready             bool
	Unschedulable     bool
	DiskPressure      bool
	MemoryPressure    bool
	PIDPressure       bool
	NetworkUnavail    bool
	Conditions        []string // human-readable condition summaries
	CPUCapacity       string   // e.g., "4"
	MemoryCapacity    string   // e.g., "8Gi"
	CPUAllocatable    string
	MemoryAllocatable string
	CPUUsage          string // from metrics server, e.g., "1200m"
	MemoryUsage       string // from metrics server, e.g., "3Gi"
	PodCount          int32
	PodCapacity       int32
	KubeletVersion    string
}

// Alert represents an anomaly detected by the watcher.
type Alert struct {
	Timestamp time.Time
	Severity  AlertSeverity
	Type      AlertType
	Message   string
	Object    string // affected resource (format: "Kind/Name" for resources, or just pod name)
	Namespace string // namespace of the affected resource
}

// AlertSeverity indicates the severity level of an alert.
type AlertSeverity string

const (
	SeverityInfo     AlertSeverity = "INFO"
	SeverityWarning  AlertSeverity = "WARNING"
	SeverityCritical AlertSeverity = "CRITICAL"
)

// AlertType categorizes the type of anomaly detected.
type AlertType string

const (
	AlertPodCrashLoop    AlertType = "CrashLoopBackOff"
	AlertPodOOMKilled    AlertType = "OOMKilled"
	AlertHighRestarts    AlertType = "HighRestartCount"
	AlertPodNotReady     AlertType = "PodNotReady"
	AlertScaleEvent      AlertType = "ScaleEvent"
	AlertDeployFailing   AlertType = "DeploymentFailing"
	AlertJobFailed       AlertType = "JobFailed"
	AlertCronJobMissed   AlertType = "CronJobMissed"
	AlertNodeNotReady    AlertType = "NodeNotReady"
	AlertDiskPressure    AlertType = "DiskPressure"
	AlertMemoryPressure  AlertType = "MemoryPressure"
	AlertPIDPressure     AlertType = "PIDPressure"
	AlertNetworkUnavail  AlertType = "NetworkUnavailable"
	AlertNodeUnschedul   AlertType = "NodeUnschedulable"
	AlertPodCapacityHigh AlertType = "PodCapacityHigh"
)

// WatchConfig holds configuration for the Kubernetes watcher.
type WatchConfig struct {
	Deployment  string
	Kind        string // Deployment, StatefulSet, DaemonSet, Job, CronJob (default: Deployment)
	Namespace   string
	Interval    time.Duration
	Window      time.Duration
	MaxLogLines int
	Kubeconfig  string // path to kubeconfig (empty = in-cluster)
}

// ResourceKind returns the effective kind, defaulting to "Deployment".
func (c *WatchConfig) ResourceKind() string {
	if c.Kind != "" {
		return c.Kind
	}
	return "Deployment"
}

// LogEntry holds a log line from a pod.
type LogEntry struct {
	Timestamp time.Time
	PodName   string
	Container string
	Line      string
	IsError   bool
}

// WatchTarget defines a single Kubernetes resource to watch with optional Prometheus scraping.
type WatchTarget struct {
	Deployment    string   `yaml:"deployment" json:"deployment"`
	Kind          string   `yaml:"kind,omitempty" json:"kind,omitempty"` // Deployment, StatefulSet, DaemonSet, Job, CronJob (default: Deployment)
	Namespace     string   `yaml:"namespace" json:"namespace"`
	MetricsPort   int      `yaml:"metricsPort,omitempty" json:"metricsPort,omitempty"`
	MetricsPath   string   `yaml:"metricsPath,omitempty" json:"metricsPath,omitempty"`
	MetricsFilter []string `yaml:"metricsFilter,omitempty" json:"metricsFilter,omitempty"`
}

// ResourceKind returns the effective resource kind, defaulting to "Deployment".
func (t WatchTarget) ResourceKind() string {
	if t.Kind != "" {
		return t.Kind
	}
	return "Deployment"
}

// Key returns a unique identifier for this target: "kind/namespace/name".
func (t WatchTarget) Key() string {
	return t.ResourceKind() + "/" + t.Namespace + "/" + t.Deployment
}

// MultiWatchConfig holds configuration for multi-deployment watching.
type MultiWatchConfig struct {
	Targets         []WatchTarget `yaml:"targets" json:"targets"`
	Interval        time.Duration `yaml:"interval" json:"interval"`
	Window          time.Duration `yaml:"window" json:"window"`
	MaxLogLines     int           `yaml:"maxLogLines" json:"maxLogLines"`
	Kubeconfig      string        `yaml:"kubeconfig,omitempty" json:"kubeconfig,omitempty"`
	MaxContextChars int           `yaml:"maxContextChars,omitempty" json:"maxContextChars,omitempty"`
}

// AppMetrics holds application-level metrics scraped from Prometheus endpoints.
type AppMetrics struct {
	Timestamp time.Time
	Metrics   map[string]float64 // metric_name -> value
}

// TargetHealthScore represents the health status of a watch target for budget allocation.
type TargetHealthScore struct {
	Key        string
	Score      int // 0 = healthy, 1 = warning, 2 = critical
	AlertCount int
}

// WatcherMetricsRecorder is the interface for recording watcher metrics.
// Implemented by metrics.WatcherMetrics to avoid circular imports.
type WatcherMetricsRecorder interface {
	ObserveCollectionDuration(target string, seconds float64)
	IncrementCollectionErrors(target string)
	IncrementAlert(target, severity, alertType string)
	SetPodsReady(namespace, deployment string, count float64)
	SetPodsDesired(namespace, deployment string, count float64)
	SetSnapshotsStored(target string, count float64)
	SetPodRestarts(target string, count float64)
}
