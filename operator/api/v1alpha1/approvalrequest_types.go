package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// BlastRadiusAssessment captures the potential impact of a remediation.
type BlastRadiusAssessment struct {
	// AffectedPods is the number of pods that will be affected.
	AffectedPods int32 `json:"affectedPods"`

	// AffectedServices is the number of services routing to affected pods.
	AffectedServices int32 `json:"affectedServices"`

	// AffectedNamespaces lists namespaces with affected resources.
	// +optional
	AffectedNamespaces []string `json:"affectedNamespaces,omitempty"`

	// RiskLevel is the assessed risk (low/medium/high/critical).
	RiskLevel string `json:"riskLevel"`

	// Description explains the blast radius assessment.
	Description string `json:"description"`
}

// ApprovalEvidence provides context for the approval decision.
type ApprovalEvidence struct {
	// AIConfidence from the AIInsight analysis (0.0-1.0).
	AIConfidence float64 `json:"aiConfidence"`

	// AIAnalysis is the AI's root cause analysis.
	// +optional
	AIAnalysis string `json:"aiAnalysis,omitempty"`

	// HistoricalSuccessRate for this action type (0.0-1.0).
	HistoricalSuccessRate float64 `json:"historicalSuccessRate"`

	// PreviousAttempts is the number of previous remediation attempts.
	PreviousAttempts int32 `json:"previousAttempts"`

	// PreflightSnapshot captures the resource state before remediation.
	// +optional
	PreflightSnapshot string `json:"preflightSnapshot,omitempty"`
}

// ApprovalDecision records a single approver's decision.
type ApprovalDecision struct {
	// Approver identifies who made the decision.
	Approver string `json:"approver"`

	// Decision is "approved" or "rejected".
	Decision string `json:"decision"`

	// Reason explains the decision.
	// +optional
	Reason string `json:"reason,omitempty"`

	// Timestamp of the decision.
	Timestamp metav1.Time `json:"timestamp"`
}

// ApprovalRequestSpec defines the desired state of ApprovalRequest.
type ApprovalRequestSpec struct {
	// IssueRef references the parent Issue.
	IssueRef IssueRef `json:"issueRef"`

	// RemediationPlanRef is the name of the RemediationPlan needing approval.
	RemediationPlanRef string `json:"remediationPlanRef"`

	// PolicyRef is the name of the ApprovalPolicy that triggered this request.
	PolicyRef string `json:"policyRef"`

	// RuleName identifies which rule in the policy matched.
	RuleName string `json:"ruleName"`

	// RequestedActions are the remediation actions needing approval.
	RequestedActions []RemediationAction `json:"requestedActions"`

	// Requester identifies who/what created this request.
	Requester string `json:"requester"`

	// BlastRadius assessment of the proposed actions.
	// +optional
	BlastRadius *BlastRadiusAssessment `json:"blastRadius,omitempty"`

	// Evidence supporting the remediation decision.
	// +optional
	Evidence *ApprovalEvidence `json:"evidence,omitempty"`

	// TimeoutMinutes before the request expires.
	// +kubebuilder:default=30
	TimeoutMinutes int32 `json:"timeoutMinutes"`

	// RequiredApprovers for quorum mode.
	// +kubebuilder:default=1
	RequiredApprovers int32 `json:"requiredApprovers"`
}

// ApprovalRequestStatus defines the observed state of ApprovalRequest.
type ApprovalRequestStatus struct {
	// State of the approval request.
	State ApprovalRequestState `json:"state"`

	// Decisions records all approver decisions.
	// +optional
	Decisions []ApprovalDecision `json:"decisions,omitempty"`

	// ApprovedAt is when the request was approved.
	// +optional
	ApprovedAt *metav1.Time `json:"approvedAt,omitempty"`

	// RejectedAt is when the request was rejected.
	// +optional
	RejectedAt *metav1.Time `json:"rejectedAt,omitempty"`

	// ExpiredAt is when the request expired.
	// +optional
	ExpiredAt *metav1.Time `json:"expiredAt,omitempty"`

	// AutoApproved indicates the request was auto-approved by policy.
	AutoApproved bool `json:"autoApproved"`

	// Conditions represent the latest available observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ar
// +kubebuilder:printcolumn:name="Issue",type="string",JSONPath=".spec.issueRef.name"
// +kubebuilder:printcolumn:name="Plan",type="string",JSONPath=".spec.remediationPlanRef"
// +kubebuilder:printcolumn:name="State",type="string",JSONPath=".status.state"
// +kubebuilder:printcolumn:name="Rule",type="string",JSONPath=".spec.ruleName"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// ApprovalRequest represents a pending approval for a remediation action.
type ApprovalRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ApprovalRequestSpec   `json:"spec,omitempty"`
	Status ApprovalRequestStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ApprovalRequestList contains a list of ApprovalRequest.
type ApprovalRequestList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ApprovalRequest `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ApprovalRequest{}, &ApprovalRequestList{})
}
