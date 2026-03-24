package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// RemediationState represents the execution state of a remediation plan.
// +kubebuilder:validation:Enum=Pending;WaitingApproval;Executing;Verifying;Completed;Failed;RolledBack
type RemediationState string

const (
	RemediationStatePending          RemediationState = "Pending"
	RemediationStateWaitingApproval  RemediationState = "WaitingApproval"
	RemediationStateExecuting        RemediationState = "Executing"
	RemediationStateVerifying        RemediationState = "Verifying"
	RemediationStateCompleted        RemediationState = "Completed"
	RemediationStateFailed           RemediationState = "Failed"
	RemediationStateRolledBack       RemediationState = "RolledBack"
)

// RemediationActionType defines the type of remediation action.
// +kubebuilder:validation:Enum=ScaleDeployment;RollbackDeployment;RestartDeployment;PatchConfig;AdjustResources;DeletePod;HelmRollback;ArgoSyncApp;AdjustHPA;RestartStatefulSetPod;CordonNode;DrainNode;ResizePVC;RotateSecret;ExecDiagnostic;UpdateIngress;PatchNetworkPolicy;ApplyManifest;ScaleStatefulSet;RestartStatefulSet;RollbackStatefulSet;AdjustStatefulSetResources;DeleteStatefulSetPod;ForceDeleteStatefulSetPod;UpdateStatefulSetStrategy;RecreateStatefulSetPVC;PartitionStatefulSetUpdate;RestartDaemonSet;RollbackDaemonSet;AdjustDaemonSetResources;DeleteDaemonSetPod;UpdateDaemonSetStrategy;PauseDaemonSetRollout;CordonAndDeleteDaemonSetPod;RetryJob;AdjustJobResources;DeleteFailedJob;SuspendJob;ResumeJob;AdjustJobParallelism;AdjustJobDeadline;AdjustJobBackoffLimit;ForceDeleteJobPods;SuspendCronJob;ResumeCronJob;TriggerCronJob;AdjustCronJobResources;AdjustCronJobSchedule;AdjustCronJobDeadline;AdjustCronJobHistory;AdjustCronJobConcurrency;DeleteCronJobActiveJobs;ReplaceCronJobTemplate;Custom
type RemediationActionType string

const (
	// --- Deployment / Generic Actions ---
	ActionScaleDeployment       RemediationActionType = "ScaleDeployment"
	ActionRollbackDeployment    RemediationActionType = "RollbackDeployment"
	ActionRestartDeployment     RemediationActionType = "RestartDeployment"
	ActionPatchConfig           RemediationActionType = "PatchConfig"
	ActionAdjustResources       RemediationActionType = "AdjustResources"
	ActionDeletePod             RemediationActionType = "DeletePod"
	ActionHelmRollback          RemediationActionType = "HelmRollback"
	ActionArgoSyncApp           RemediationActionType = "ArgoSyncApp"
	ActionAdjustHPA             RemediationActionType = "AdjustHPA"
	ActionRestartStatefulSetPod RemediationActionType = "RestartStatefulSetPod"
	ActionCordonNode            RemediationActionType = "CordonNode"
	ActionDrainNode             RemediationActionType = "DrainNode"
	ActionResizePVC             RemediationActionType = "ResizePVC"
	ActionRotateSecret          RemediationActionType = "RotateSecret"
	ActionExecDiagnostic        RemediationActionType = "ExecDiagnostic"
	ActionUpdateIngress         RemediationActionType = "UpdateIngress"
	ActionPatchNetworkPolicy    RemediationActionType = "PatchNetworkPolicy"
	ActionApplyManifest         RemediationActionType = "ApplyManifest"

	// --- StatefulSet Actions ---
	ActionScaleStatefulSet           RemediationActionType = "ScaleStatefulSet"
	ActionRestartStatefulSet         RemediationActionType = "RestartStatefulSet"
	ActionRollbackStatefulSet        RemediationActionType = "RollbackStatefulSet"
	ActionAdjustStatefulSetResources RemediationActionType = "AdjustStatefulSetResources"
	ActionDeleteStatefulSetPod       RemediationActionType = "DeleteStatefulSetPod"
	ActionForceDeleteStatefulSetPod  RemediationActionType = "ForceDeleteStatefulSetPod"
	ActionUpdateStatefulSetStrategy  RemediationActionType = "UpdateStatefulSetStrategy"
	ActionRecreateStatefulSetPVC     RemediationActionType = "RecreateStatefulSetPVC"
	ActionPartitionStatefulSetUpdate RemediationActionType = "PartitionStatefulSetUpdate"

	// --- DaemonSet Actions ---
	ActionRestartDaemonSet            RemediationActionType = "RestartDaemonSet"
	ActionRollbackDaemonSet           RemediationActionType = "RollbackDaemonSet"
	ActionAdjustDaemonSetResources    RemediationActionType = "AdjustDaemonSetResources"
	ActionDeleteDaemonSetPod          RemediationActionType = "DeleteDaemonSetPod"
	ActionUpdateDaemonSetStrategy     RemediationActionType = "UpdateDaemonSetStrategy"
	ActionPauseDaemonSetRollout       RemediationActionType = "PauseDaemonSetRollout"
	ActionCordonAndDeleteDaemonSetPod RemediationActionType = "CordonAndDeleteDaemonSetPod"

	// --- Job Actions ---
	ActionRetryJob              RemediationActionType = "RetryJob"
	ActionAdjustJobResources    RemediationActionType = "AdjustJobResources"
	ActionDeleteFailedJob       RemediationActionType = "DeleteFailedJob"
	ActionSuspendJob            RemediationActionType = "SuspendJob"
	ActionResumeJob             RemediationActionType = "ResumeJob"
	ActionAdjustJobParallelism  RemediationActionType = "AdjustJobParallelism"
	ActionAdjustJobDeadline     RemediationActionType = "AdjustJobDeadline"
	ActionAdjustJobBackoffLimit RemediationActionType = "AdjustJobBackoffLimit"
	ActionForceDeleteJobPods    RemediationActionType = "ForceDeleteJobPods"

	// --- CronJob Actions ---
	ActionSuspendCronJob           RemediationActionType = "SuspendCronJob"
	ActionResumeCronJob            RemediationActionType = "ResumeCronJob"
	ActionTriggerCronJob           RemediationActionType = "TriggerCronJob"
	ActionAdjustCronJobResources   RemediationActionType = "AdjustCronJobResources"
	ActionAdjustCronJobSchedule    RemediationActionType = "AdjustCronJobSchedule"
	ActionAdjustCronJobDeadline    RemediationActionType = "AdjustCronJobDeadline"
	ActionAdjustCronJobHistory     RemediationActionType = "AdjustCronJobHistory"
	ActionAdjustCronJobConcurrency RemediationActionType = "AdjustCronJobConcurrency"
	ActionDeleteCronJobActiveJobs  RemediationActionType = "DeleteCronJobActiveJobs"
	ActionReplaceCronJobTemplate   RemediationActionType = "ReplaceCronJobTemplate"

	// --- Fallback ---
	ActionCustom RemediationActionType = "Custom"
)

// RemediationAction defines a single remediation step.
type RemediationAction struct {
	// Type of the remediation action.
	Type RemediationActionType `json:"type"`

	// Params are key-value parameters for the action.
	// +optional
	Params map[string]string `json:"params,omitempty"`
}

// AgenticStep records one turn of the AI-driven remediation loop.
type AgenticStep struct {
	// StepNumber is 1-based, incremented each reconcile cycle.
	StepNumber int32 `json:"stepNumber"`

	// AIMessage is the AI's reasoning for this step.
	AIMessage string `json:"aiMessage"`

	// Action is the action the AI decided to take (nil if observation-only or resolved).
	// +optional
	Action *RemediationAction `json:"action,omitempty"`

	// Observation is the result of executing the action.
	// +optional
	Observation string `json:"observation,omitempty"`

	// Timestamp is when this step was recorded.
	Timestamp metav1.Time `json:"timestamp"`
}

// RemediationPlanSpec defines the desired state of a RemediationPlan.
type RemediationPlanSpec struct {
	// IssueRef references the parent Issue.
	IssueRef IssueRef `json:"issueRef"`

	// Attempt number (1-based).
	Attempt int32 `json:"attempt"`

	// Strategy describes the remediation approach.
	Strategy string `json:"strategy"`

	// Actions to execute (ignored when AgenticMode is true).
	// +optional
	Actions []RemediationAction `json:"actions,omitempty"`

	// SafetyConstraints that must be respected.
	// +optional
	SafetyConstraints []string `json:"safetyConstraints,omitempty"`

	// AgenticMode enables AI-driven step-by-step remediation.
	// When true, Actions is ignored and the AI decides each step.
	// +optional
	AgenticMode bool `json:"agenticMode,omitempty"`

	// AgenticHistory records all steps of the agentic loop.
	// Stored in spec (not status) because it is the authoritative
	// conversation history sent to the AI on every step.
	// +optional
	AgenticHistory []AgenticStep `json:"agenticHistory,omitempty"`

	// AgenticMaxSteps is the maximum number of agentic steps before forced failure.
	// +kubebuilder:default=10
	// +optional
	AgenticMaxSteps int32 `json:"agenticMaxSteps,omitempty"`
}

// ResourceSnapshot captures the full restorable state of a Kubernetes resource
// before remediation modifies it. Used for automatic rollback on failure.
type ResourceSnapshot struct {
	// ResourceKind is the kind of resource (Deployment, StatefulSet, etc.).
	ResourceKind string `json:"resourceKind"`

	// ResourceName is the name of the resource.
	ResourceName string `json:"resourceName"`

	// Namespace of the resource.
	Namespace string `json:"namespace"`

	// Replicas before modification.
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// ContainerImages maps container name to image before modification.
	// +optional
	ContainerImages map[string]string `json:"containerImages,omitempty"`

	// ContainerResources maps container name to its resources before modification.
	// +optional
	ContainerResources map[string]ContainerResourceSnapshot `json:"containerResources,omitempty"`

	// HPASpec captures HPA state before modification.
	// +optional
	HPAMinReplicas *int32 `json:"hpaMinReplicas,omitempty"`
	// +optional
	HPAMaxReplicas *int32 `json:"hpaMaxReplicas,omitempty"`

	// NodeUnschedulable captures the node's schedulable state before cordon.
	// +optional
	NodeUnschedulable *bool `json:"nodeUnschedulable,omitempty"`

	// Annotations captures relevant annotations before modification.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`

	// --- Job/CronJob state ---

	// Suspend captures the suspend state (Job, CronJob).
	// +optional
	Suspend *bool `json:"suspend,omitempty"`

	// ActiveDeadlineSeconds captures job deadline before modification.
	// +optional
	ActiveDeadlineSeconds *int64 `json:"activeDeadlineSeconds,omitempty"`

	// BackoffLimit captures job backoff limit before modification.
	// +optional
	BackoffLimit *int32 `json:"backoffLimit,omitempty"`

	// Parallelism captures job parallelism before modification.
	// +optional
	Parallelism *int32 `json:"parallelism,omitempty"`

	// Schedule captures cron schedule before modification.
	// +optional
	Schedule string `json:"schedule,omitempty"`

	// ConcurrencyPolicy captures cronjob concurrency before modification.
	// +optional
	ConcurrencyPolicy string `json:"concurrencyPolicy,omitempty"`

	// StartingDeadlineSeconds captures cronjob deadline.
	// +optional
	StartingDeadlineSeconds *int64 `json:"startingDeadlineSeconds,omitempty"`

	// SuccessfulJobsHistoryLimit captures history limit.
	// +optional
	SuccessfulJobsHistoryLimit *int32 `json:"successfulJobsHistoryLimit,omitempty"`

	// FailedJobsHistoryLimit captures history limit.
	// +optional
	FailedJobsHistoryLimit *int32 `json:"failedJobsHistoryLimit,omitempty"`

	// --- StatefulSet/DaemonSet update strategy state ---

	// UpdateStrategyType captures StatefulSet/DaemonSet update strategy type.
	// +optional
	UpdateStrategyType string `json:"updateStrategyType,omitempty"`

	// Partition captures StatefulSet partition value.
	// +optional
	Partition *int32 `json:"partition,omitempty"`

	// MaxUnavailable captures DaemonSet maxUnavailable value.
	// +optional
	MaxUnavailable string `json:"maxUnavailable,omitempty"`

	// FullSpec stores a JSON-serialized copy of the full spec for complex rollbacks.
	// +optional
	FullSpec string `json:"fullSpec,omitempty"`

	// CapturedAt is when this snapshot was taken.
	CapturedAt metav1.Time `json:"capturedAt"`
}

// ContainerResourceSnapshot captures a single container's resource requests/limits.
type ContainerResourceSnapshot struct {
	// CPURequest before modification.
	// +optional
	CPURequest string `json:"cpuRequest,omitempty"`
	// CPULimit before modification.
	// +optional
	CPULimit string `json:"cpuLimit,omitempty"`
	// MemoryRequest before modification.
	// +optional
	MemoryRequest string `json:"memoryRequest,omitempty"`
	// MemoryLimit before modification.
	// +optional
	MemoryLimit string `json:"memoryLimit,omitempty"`
}

// ActionCheckpoint records the state after each individual action execution,
// enabling partial rollback when a multi-action plan fails midway.
type ActionCheckpoint struct {
	// ActionIndex is the 0-based index of the action that was executed.
	ActionIndex int32 `json:"actionIndex"`

	// ActionType is the type of the action.
	ActionType RemediationActionType `json:"actionType"`

	// Success indicates whether this action succeeded.
	Success bool `json:"success"`

	// SnapshotBefore is the resource state captured before THIS action.
	// +optional
	SnapshotBefore *ResourceSnapshot `json:"snapshotBefore,omitempty"`

	// Timestamp of execution.
	Timestamp metav1.Time `json:"timestamp"`
}

// EvidenceItem is a piece of evidence collected during remediation.
type EvidenceItem struct {
	// Type of evidence (log, metric_snapshot, event, preflight_snapshot, rollback).
	Type string `json:"type"`

	// Data content.
	Data string `json:"data"`

	// Timestamp when collected.
	Timestamp metav1.Time `json:"timestamp"`
}

// RemediationPlanStatus defines the observed state of a RemediationPlan.
type RemediationPlanStatus struct {
	// State of the remediation plan execution.
	State RemediationState `json:"state"`

	// StartedAt is when execution began.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// ActionsCompletedAt is when all actions finished executing and verification began.
	// +optional
	ActionsCompletedAt *metav1.Time `json:"actionsCompletedAt,omitempty"`

	// CompletedAt is when execution finished (after verification).
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// Result describes the outcome.
	// +optional
	Result string `json:"result,omitempty"`

	// Evidence collected during execution.
	// +optional
	Evidence []EvidenceItem `json:"evidence,omitempty"`

	// AgenticStepCount is the number of agentic steps completed.
	// +optional
	AgenticStepCount int32 `json:"agenticStepCount,omitempty"`

	// AgenticStartedAt is when the agentic loop first started.
	// +optional
	AgenticStartedAt *metav1.Time `json:"agenticStartedAt,omitempty"`

	// PreflightSnapshot is the full restorable state of the resource before any actions.
	// Used for automatic rollback on failure.
	// +optional
	PreflightSnapshot *ResourceSnapshot `json:"preflightSnapshot,omitempty"`

	// ActionCheckpoints record the state before each action for partial rollback.
	// +optional
	ActionCheckpoints []ActionCheckpoint `json:"actionCheckpoints,omitempty"`

	// RollbackPerformed indicates whether an automatic rollback was executed.
	// +optional
	RollbackPerformed bool `json:"rollbackPerformed,omitempty"`

	// RollbackResult describes the outcome of the automatic rollback.
	// +optional
	RollbackResult string `json:"rollbackResult,omitempty"`

	// PostFailureHealthy indicates whether the resource is healthy after failure+rollback.
	// +optional
	PostFailureHealthy *bool `json:"postFailureHealthy,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=rp
// +kubebuilder:printcolumn:name="Issue",type="string",JSONPath=".spec.issueRef.name"
// +kubebuilder:printcolumn:name="Attempt",type="integer",JSONPath=".spec.attempt"
// +kubebuilder:printcolumn:name="State",type="string",JSONPath=".status.state"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// RemediationPlan records a remediation attempt for an Issue.
type RemediationPlan struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RemediationPlanSpec   `json:"spec,omitempty"`
	Status RemediationPlanStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// RemediationPlanList contains a list of RemediationPlan.
type RemediationPlanList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RemediationPlan `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RemediationPlan{}, &RemediationPlanList{})
}
