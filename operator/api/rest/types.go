package rest

import (
	"time"
)

// APIResponse is the standard envelope for all REST API responses.
type APIResponse struct {
	APIVersion string      `json:"apiVersion"`
	Kind       string      `json:"kind"`
	Metadata   *ListMeta   `json:"metadata,omitempty"`
	Items      interface{} `json:"items,omitempty"`

	// Single-resource fields (non-list responses)
	Spec   interface{} `json:"spec,omitempty"`
	Status interface{} `json:"status,omitempty"`

	// Resource metadata for single-resource responses
	ResourceMeta *ResourceMeta `json:"resourceMeta,omitempty"`
}

// ListMeta carries pagination metadata.
type ListMeta struct {
	TotalCount int `json:"totalCount"`
	Page       int `json:"page"`
	PageSize   int `json:"pageSize"`
}

// ResourceMeta carries metadata for a single resource.
type ResourceMeta struct {
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace"`
	UID               string            `json:"uid,omitempty"`
	CreationTimestamp string            `json:"creationTimestamp,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
	Annotations       map[string]string `json:"annotations,omitempty"`
}

// ErrorResponse is the standard error envelope.
type ErrorResponse struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Error      string `json:"error"`
	Code       int    `json:"code"`
	Message    string `json:"message"`
}

// IncidentItem is the REST representation of an Issue.
type IncidentItem struct {
	Name                   string            `json:"name"`
	Namespace              string            `json:"namespace"`
	Severity               string            `json:"severity"`
	Source                 string            `json:"source"`
	SignalType             string            `json:"signalType,omitempty"`
	Resource               ResourceRefItem   `json:"resource"`
	Description            string            `json:"description"`
	RiskScore              int32             `json:"riskScore"`
	State                  string            `json:"state"`
	DetectedAt             *string           `json:"detectedAt,omitempty"`
	ResolvedAt             *string           `json:"resolvedAt,omitempty"`
	Resolution             string            `json:"resolution,omitempty"`
	RemediationAttempts    int32             `json:"remediationAttempts"`
	MaxRemediationAttempts int32             `json:"maxRemediationAttempts"`
	CreationTimestamp      string            `json:"creationTimestamp"`
	Labels                 map[string]string `json:"labels,omitempty"`
	Annotations            map[string]string `json:"annotations,omitempty"`
}

// ResourceRefItem is the REST representation of a ResourceRef.
type ResourceRefItem struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

// RemediationItem is the REST representation of a RemediationPlan.
type RemediationItem struct {
	Name              string       `json:"name"`
	Namespace         string       `json:"namespace"`
	IssueRef          string       `json:"issueRef"`
	Attempt           int32        `json:"attempt"`
	Strategy          string       `json:"strategy"`
	State             string       `json:"state"`
	StartedAt         *string      `json:"startedAt,omitempty"`
	CompletedAt       *string      `json:"completedAt,omitempty"`
	Result            string       `json:"result,omitempty"`
	AgenticMode       bool         `json:"agenticMode"`
	AgenticStepCount  int32        `json:"agenticStepCount"`
	Actions           []ActionItem `json:"actions,omitempty"`
	CreationTimestamp string       `json:"creationTimestamp"`
}

// ActionItem is the REST representation of a RemediationAction.
type ActionItem struct {
	Type   string            `json:"type"`
	Params map[string]string `json:"params,omitempty"`
}

// PostMortemItem is the REST representation of a PostMortem.
type PostMortemItem struct {
	Name              string             `json:"name"`
	Namespace         string             `json:"namespace"`
	IssueRef          string             `json:"issueRef"`
	Resource          ResourceRefItem    `json:"resource"`
	Severity          string             `json:"severity"`
	State             string             `json:"state"`
	Summary           string             `json:"summary,omitempty"`
	RootCause         string             `json:"rootCause,omitempty"`
	Impact            string             `json:"impact,omitempty"`
	Duration          string             `json:"duration,omitempty"`
	LessonsLearned    []string           `json:"lessonsLearned,omitempty"`
	PreventionActions []string           `json:"preventionActions,omitempty"`
	Timeline          []TimelineItem     `json:"timeline,omitempty"`
	ActionsExecuted   []ActionRecordItem `json:"actionsExecuted,omitempty"`
	GeneratedAt       *string            `json:"generatedAt,omitempty"`
	ReviewedAt        *string            `json:"reviewedAt,omitempty"`
	CreationTimestamp string             `json:"creationTimestamp"`
}

// TimelineItem is the REST representation of a TimelineEvent.
type TimelineItem struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Detail    string `json:"detail"`
}

// ActionRecordItem is the REST representation of an ActionRecord.
type ActionRecordItem struct {
	Action    string            `json:"action"`
	Params    map[string]string `json:"params,omitempty"`
	Result    string            `json:"result"`
	Detail    string            `json:"detail"`
	Timestamp string            `json:"timestamp"`
}

// RunbookItem is the REST representation of a Runbook.
type RunbookItem struct {
	Name              string             `json:"name"`
	Namespace         string             `json:"namespace"`
	Description       string             `json:"description,omitempty"`
	Trigger           RunbookTriggerItem `json:"trigger"`
	Steps             []RunbookStepItem  `json:"steps"`
	MaxAttempts       int32              `json:"maxAttempts"`
	CreationTimestamp string             `json:"creationTimestamp"`
}

// RunbookTriggerItem is the REST representation of a RunbookTrigger.
type RunbookTriggerItem struct {
	SignalType   string `json:"signalType"`
	Severity     string `json:"severity"`
	ResourceKind string `json:"resourceKind"`
}

// RunbookStepItem is the REST representation of a RunbookStep.
type RunbookStepItem struct {
	Name        string            `json:"name"`
	Action      string            `json:"action"`
	Description string            `json:"description,omitempty"`
	Params      map[string]string `json:"params,omitempty"`
}

// RunbookCreateRequest is the body for creating/updating a Runbook.
type RunbookCreateRequest struct {
	Name        string             `json:"name"`
	Namespace   string             `json:"namespace"`
	Description string             `json:"description,omitempty"`
	Trigger     RunbookTriggerItem `json:"trigger"`
	Steps       []RunbookStepItem  `json:"steps"`
	MaxAttempts int32              `json:"maxAttempts,omitempty"`
	Labels      map[string]string  `json:"labels,omitempty"`
}

// ApprovalItem is the REST representation of an ApprovalRequest (unstructured).
type ApprovalItem struct {
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace"`
	Resource          string            `json:"resource,omitempty"`
	Action            string            `json:"action,omitempty"`
	Reason            string            `json:"reason,omitempty"`
	RequestedBy       string            `json:"requestedBy,omitempty"`
	State             string            `json:"state,omitempty"`
	ApprovedBy        string            `json:"approvedBy,omitempty"`
	RejectedBy        string            `json:"rejectedBy,omitempty"`
	DecisionReason    string            `json:"decisionReason,omitempty"`
	DecidedAt         *string           `json:"decidedAt,omitempty"`
	CreationTimestamp string            `json:"creationTimestamp"`
	Labels            map[string]string `json:"labels,omitempty"`
}

// ApprovalDecisionRequest is the body for approve/reject.
type ApprovalDecisionRequest struct {
	Approver string `json:"approver"`
	Reason   string `json:"reason"`
}

// SLOItem is the REST representation of a ServiceLevelObjective (unstructured).
type SLOItem struct {
	Name                 string  `json:"name"`
	Namespace            string  `json:"namespace"`
	Service              string  `json:"service,omitempty"`
	SLI                  string  `json:"sli,omitempty"`
	Target               float64 `json:"target,omitempty"`
	Window               string  `json:"window,omitempty"`
	CurrentValue         float64 `json:"currentValue,omitempty"`
	ErrorBudgetTotal     float64 `json:"errorBudgetTotal,omitempty"`
	ErrorBudgetUsed      float64 `json:"errorBudgetUsed,omitempty"`
	ErrorBudgetRemaining float64 `json:"errorBudgetRemaining,omitempty"`
	State                string  `json:"state,omitempty"`
	CreationTimestamp    string  `json:"creationTimestamp"`
}

// SLOBudgetItem provides detailed error budget information.
type SLOBudgetItem struct {
	Name                 string  `json:"name"`
	Target               float64 `json:"target"`
	CurrentValue         float64 `json:"currentValue"`
	ErrorBudgetTotal     float64 `json:"errorBudgetTotal"`
	ErrorBudgetUsed      float64 `json:"errorBudgetUsed"`
	ErrorBudgetRemaining float64 `json:"errorBudgetRemaining"`
	BurnRate             float64 `json:"burnRate"`
	Window               string  `json:"window"`
	State                string  `json:"state"`
}

// ClusterItem is the REST representation of a ClusterRegistration (unstructured).
type ClusterItem struct {
	Name               string            `json:"name"`
	Namespace          string            `json:"namespace"`
	DisplayName        string            `json:"displayName,omitempty"`
	Region             string            `json:"region,omitempty"`
	Environment        string            `json:"environment,omitempty"`
	Tier               string            `json:"tier,omitempty"`
	Connected          bool              `json:"connected"`
	Version            string            `json:"version,omitempty"`
	NodeCount          int64             `json:"nodeCount,omitempty"`
	NamespaceCount     int64             `json:"namespaceCount,omitempty"`
	ActiveIssues       int64             `json:"activeIssues,omitempty"`
	ActiveRemediations int64             `json:"activeRemediations,omitempty"`
	LastHealthCheck    *string           `json:"lastHealthCheck,omitempty"`
	CreationTimestamp  string            `json:"creationTimestamp"`
	Labels             map[string]string `json:"labels,omitempty"`
}

// GlobalClusterStatus is the aggregated cross-cluster status.
type GlobalClusterStatus struct {
	TotalClusters    int           `json:"totalClusters"`
	HealthyClusters  int           `json:"healthyClusters"`
	DegradedClusters int           `json:"degradedClusters"`
	OfflineClusters  int           `json:"offlineClusters"`
	Clusters         []ClusterItem `json:"clusters"`
}

// AuditEventItem is the REST representation of an AuditEvent (unstructured).
type AuditEventItem struct {
	Name              string `json:"name"`
	Namespace         string `json:"namespace"`
	EventType         string `json:"eventType,omitempty"`
	Severity          string `json:"severity,omitempty"`
	ActorType         string `json:"actorType,omitempty"`
	ActorName         string `json:"actorName,omitempty"`
	ResourceKind      string `json:"resourceKind,omitempty"`
	ResourceName      string `json:"resourceName,omitempty"`
	ResourceNamespace string `json:"resourceNamespace,omitempty"`
	CorrelationID     string `json:"correlationId,omitempty"`
	Detail            string `json:"detail,omitempty"`
	Timestamp         string `json:"timestamp,omitempty"`
	CreationTimestamp string `json:"creationTimestamp"`
}

// AnalyticsSummary provides an overview of AIOps metrics.
type AnalyticsSummary struct {
	TotalIssues            int            `json:"totalIssues"`
	OpenIssues             int            `json:"openIssues"`
	ResolvedIssues         int            `json:"resolvedIssues"`
	CriticalIssues         int            `json:"criticalIssues"`
	TotalRemediations      int            `json:"totalRemediations"`
	SuccessfulRemediations int            `json:"successfulRemediations"`
	FailedRemediations     int            `json:"failedRemediations"`
	TotalPostMortems       int            `json:"totalPostMortems"`
	TotalRunbooks          int            `json:"totalRunbooks"`
	TotalSLOs              int            `json:"totalSLOs"`
	SLOsAtRisk             int            `json:"slosAtRisk"`
	PendingApprovals       int            `json:"pendingApprovals"`
	AvgRiskScore           float64        `json:"avgRiskScore"`
	SeverityBreakdown      map[string]int `json:"severityBreakdown"`
}

// MTTMetric represents a mean-time metric data point.
type MTTMetric struct {
	Date  string  `json:"date"`
	Value float64 `json:"value"` // in seconds
	Count int     `json:"count"` // number of samples
}

// TrendPoint represents an issue trend data point.
type TrendPoint struct {
	Date       string         `json:"date"`
	Total      int            `json:"total"`
	BySeverity map[string]int `json:"bySeverity"`
}

// TopResource represents a frequently incident-prone resource.
type TopResource struct {
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	Namespace     string `json:"namespace"`
	IncidentCount int    `json:"incidentCount"`
	LastIncident  string `json:"lastIncident"`
}

// RemediationStat represents remediation success statistics per action type.
type RemediationStat struct {
	Action      string  `json:"action"`
	Total       int     `json:"total"`
	Successful  int     `json:"successful"`
	Failed      int     `json:"failed"`
	SuccessRate float64 `json:"successRate"`
	AvgDuration float64 `json:"avgDuration"` // seconds
}

// SnoozeRequest is the body for snoozing an incident.
type SnoozeRequest struct {
	Duration string `json:"duration"` // e.g., "1h", "30m"
}

// ReviewRequest is the body for marking a post-mortem in review or closing it.
type ReviewRequest struct {
	Reviewer string `json:"reviewer,omitempty"`
	Notes    string `json:"notes,omitempty"`
}

// HealthResponse is the response for health/readiness endpoints.
type HealthResponse struct {
	Status    string `json:"status"`
	Timestamp string `json:"timestamp"`
}

// requestContext holds parsed authentication info for a request.
type requestContext struct {
	Role   string
	APIKey string
}

// paginationParams holds parsed pagination parameters.
type paginationParams struct {
	Page     int
	PageSize int
}

// timeRangeParams holds parsed time range filter parameters.
type timeRangeParams struct {
	From *time.Time
	To   *time.Time
}
