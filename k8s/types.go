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
