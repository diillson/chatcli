/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package k8s

import "time"

// ResourceSnapshot captures the state of monitored resources at a point in time.
type ResourceSnapshot struct {
	Timestamp  time.Time
	Deployment DeploymentStatus
	Pods       []PodStatus
	Events     []K8sEvent
	HPA        *HPAStatus
	AppMetrics *AppMetrics // application-level Prometheus metrics (nil if not configured)
}

// DeploymentStatus holds deployment-level information.
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

// Alert represents an anomaly detected by the watcher.
type Alert struct {
	Timestamp time.Time
	Severity  AlertSeverity
	Type      AlertType
	Message   string
	Object    string // affected resource
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
	AlertPodCrashLoop  AlertType = "CrashLoopBackOff"
	AlertPodOOMKilled  AlertType = "OOMKilled"
	AlertHighRestarts  AlertType = "HighRestartCount"
	AlertPodNotReady   AlertType = "PodNotReady"
	AlertScaleEvent    AlertType = "ScaleEvent"
	AlertDeployFailing AlertType = "DeploymentFailing"
)

// WatchConfig holds configuration for the Kubernetes watcher.
type WatchConfig struct {
	Deployment  string
	Namespace   string
	Interval    time.Duration
	Window      time.Duration
	MaxLogLines int
	Kubeconfig  string // path to kubeconfig (empty = in-cluster)
}

// LogEntry holds a log line from a pod.
type LogEntry struct {
	Timestamp time.Time
	PodName   string
	Container string
	Line      string
	IsError   bool
}

// WatchTarget defines a single deployment to watch with optional Prometheus scraping.
type WatchTarget struct {
	Deployment    string   `yaml:"deployment" json:"deployment"`
	Namespace     string   `yaml:"namespace" json:"namespace"`
	MetricsPort   int      `yaml:"metricsPort,omitempty" json:"metricsPort,omitempty"`
	MetricsPath   string   `yaml:"metricsPath,omitempty" json:"metricsPath,omitempty"`
	MetricsFilter []string `yaml:"metricsFilter,omitempty" json:"metricsFilter,omitempty"`
}

// Key returns a unique identifier for this target: "namespace/deployment".
func (t WatchTarget) Key() string {
	return t.Namespace + "/" + t.Deployment
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
