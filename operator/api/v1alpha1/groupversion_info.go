// Package v1alpha1 contains API Schema definitions for the chatcli v1alpha1 API group.
// +kubebuilder:object:generate=true
// +groupName=platform.chatcli.io
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is group version used to register these objects.
	GroupVersion = schema.GroupVersion{Group: "platform.chatcli.io", Version: "v1alpha1"}

	// SchemeBuilder is used to add go types to the GroupVersionResource scheme.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion,
		&AIInsight{}, &AIInsightList{},
		&Anomaly{}, &AnomalyList{},
		&ApprovalPolicy{}, &ApprovalPolicyList{},
		&ApprovalRequest{}, &ApprovalRequestList{},
		&AuditEvent{}, &AuditEventList{},
		&ChaosExperiment{}, &ChaosExperimentList{},
		&ClusterRegistration{}, &ClusterRegistrationList{},
		&EscalationPolicy{}, &EscalationPolicyList{},
		&IncidentSLA{}, &IncidentSLAList{},
		&Instance{}, &InstanceList{},
		&Issue{}, &IssueList{},
		&NotificationPolicy{}, &NotificationPolicyList{},
		&PostMortem{}, &PostMortemList{},
		&RemediationPlan{}, &RemediationPlanList{},
		&Runbook{}, &RunbookList{},
		&ServiceLevelObjective{}, &ServiceLevelObjectiveList{},
		&SourceRepository{}, &SourceRepositoryList{},
	)
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}
