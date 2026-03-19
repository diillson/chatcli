package controllers

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

type RCAEnricher struct {
	client client.Client
}

type RCAContext struct {
	RecentDeployments   []DeploymentChange
	RecentConfigChanges []ConfigChange
	RelatedIssues       []RelatedIssue
	DependencyStatus    []DependencyInfo
	TimeCorrelation     string
	PossibleCauses      []string
}

type DeploymentChange struct {
	Timestamp   time.Time
	Revision    int
	ImageBefore string
	ImageAfter  string
	ChangedBy   string
}

type ConfigChange struct {
	ConfigMapName string
	Timestamp     time.Time
	ChangedKeys   []string
}

type RelatedIssue struct {
	Name     string
	Severity platformv1alpha1.IssueSeverity
	Resource platformv1alpha1.ResourceRef
	State    platformv1alpha1.IssueState
}

type DependencyInfo struct {
	ServiceName string
	Namespace   string
	Healthy     bool
	Endpoints   int32
}

func NewRCAEnricher(c client.Client) *RCAEnricher {
	return &RCAEnricher{client: c}
}

func (e *RCAEnricher) EnrichIssueContext(ctx context.Context, issue *platformv1alpha1.Issue) (*RCAContext, error) {
	rca := &RCAContext{}
	resource := issue.Spec.Resource
	detectedAt := issue.CreationTimestamp.Time
	if issue.Status.DetectedAt != nil {
		detectedAt = issue.Status.DetectedAt.Time
	}

	// 1. Recent deployment changes via ReplicaSets
	e.findDeploymentChanges(ctx, resource, detectedAt, rca)

	// 2. Recent config changes via Events
	e.findConfigChanges(ctx, resource, detectedAt, rca)

	// 3. Related active issues
	e.findRelatedIssues(ctx, issue, rca)

	// 4. Dependency status via Services/EndpointSlices
	e.findDependencies(ctx, resource, rca)

	// 5. Time correlation
	e.correlateTimestamps(detectedAt, rca)

	// 6. Rank possible causes
	e.rankCauses(rca)

	return rca, nil
}

func (e *RCAEnricher) findDeploymentChanges(ctx context.Context, resource platformv1alpha1.ResourceRef, detectedAt time.Time, rca *RCAContext) {
	var rsList appsv1.ReplicaSetList
	if err := e.client.List(ctx, &rsList, client.InNamespace(resource.Namespace)); err != nil {
		return
	}

	type rsInfo struct {
		revision int
		rs       appsv1.ReplicaSet
	}
	var owned []rsInfo
	for i := range rsList.Items {
		rs := &rsList.Items[i]
		for _, ref := range rs.OwnerReferences {
			if ref.Kind == "Deployment" && ref.Name == resource.Name {
				rev := 0
				if revStr, ok := rs.Annotations["deployment.kubernetes.io/revision"]; ok {
					fmt.Sscanf(revStr, "%d", &rev)
				}
				owned = append(owned, rsInfo{revision: rev, rs: *rs})
				break
			}
		}
	}

	sort.Slice(owned, func(i, j int) bool { return owned[i].revision > owned[j].revision })

	window := detectedAt.Add(-30 * time.Minute)
	for i := 0; i < len(owned)-1; i++ {
		current := owned[i]
		previous := owned[i+1]
		createdAt := current.rs.CreationTimestamp.Time
		if createdAt.Before(window) {
			continue
		}

		var imgBefore, imgAfter string
		if len(previous.rs.Spec.Template.Spec.Containers) > 0 {
			imgBefore = previous.rs.Spec.Template.Spec.Containers[0].Image
		}
		if len(current.rs.Spec.Template.Spec.Containers) > 0 {
			imgAfter = current.rs.Spec.Template.Spec.Containers[0].Image
		}

		changedBy := ""
		if current.rs.Annotations != nil {
			changedBy = current.rs.Annotations["kubernetes.io/change-cause"]
		}

		rca.RecentDeployments = append(rca.RecentDeployments, DeploymentChange{
			Timestamp: createdAt, Revision: current.revision,
			ImageBefore: imgBefore, ImageAfter: imgAfter, ChangedBy: changedBy,
		})
	}
}

func (e *RCAEnricher) findConfigChanges(ctx context.Context, resource platformv1alpha1.ResourceRef, detectedAt time.Time, rca *RCAContext) {
	var events corev1.EventList
	if err := e.client.List(ctx, &events, client.InNamespace(resource.Namespace)); err != nil {
		return
	}

	window := detectedAt.Add(-30 * time.Minute)
	for _, ev := range events.Items {
		evTime := ev.LastTimestamp.Time
		if ev.EventTime.Time.After(evTime) {
			evTime = ev.EventTime.Time
		}
		if evTime.Before(window) {
			continue
		}
		if ev.InvolvedObject.Kind == "ConfigMap" && (ev.Reason == "Updated" || ev.Reason == "Modified") {
			rca.RecentConfigChanges = append(rca.RecentConfigChanges, ConfigChange{
				ConfigMapName: ev.InvolvedObject.Name,
				Timestamp:     evTime,
			})
		}
	}
}

func (e *RCAEnricher) findRelatedIssues(ctx context.Context, issue *platformv1alpha1.Issue, rca *RCAContext) {
	var issues platformv1alpha1.IssueList
	if err := e.client.List(ctx, &issues, client.InNamespace(issue.Namespace)); err != nil {
		return
	}
	for _, iss := range issues.Items {
		if iss.Name == issue.Name || isTerminalIssueState(iss.Status.State) {
			continue
		}
		rca.RelatedIssues = append(rca.RelatedIssues, RelatedIssue{
			Name: iss.Name, Severity: iss.Spec.Severity, Resource: iss.Spec.Resource, State: iss.Status.State,
		})
	}
}

func (e *RCAEnricher) findDependencies(ctx context.Context, resource platformv1alpha1.ResourceRef, rca *RCAContext) {
	// Get deployment to extract pod labels
	var deploy appsv1.Deployment
	if err := e.client.Get(ctx, client.ObjectKey{Name: resource.Name, Namespace: resource.Namespace}, &deploy); err != nil {
		return
	}

	// Find services that select these pods
	var services corev1.ServiceList
	if err := e.client.List(ctx, &services, client.InNamespace(resource.Namespace)); err != nil {
		return
	}

	for _, svc := range services.Items {
		if len(svc.Spec.Selector) == 0 {
			continue
		}
		matches := true
		for k, v := range svc.Spec.Selector {
			if deploy.Spec.Template.Labels[k] != v {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}

		// Check endpoint health
		var epSlices discoveryv1.EndpointSliceList
		if err := e.client.List(ctx, &epSlices, client.InNamespace(resource.Namespace),
			client.MatchingLabels{"kubernetes.io/service-name": svc.Name}); err != nil {
			continue
		}

		var readyCount int32
		for _, eps := range epSlices.Items {
			for _, ep := range eps.Endpoints {
				if ep.Conditions.Ready != nil && *ep.Conditions.Ready {
					readyCount++
				}
			}
		}

		rca.DependencyStatus = append(rca.DependencyStatus, DependencyInfo{
			ServiceName: svc.Name, Namespace: svc.Namespace,
			Healthy: readyCount > 0, Endpoints: readyCount,
		})
	}
}

func (e *RCAEnricher) correlateTimestamps(detectedAt time.Time, rca *RCAContext) {
	var correlations []string
	for _, dc := range rca.RecentDeployments {
		diff := detectedAt.Sub(dc.Timestamp)
		if diff >= 0 && diff <= 10*time.Minute {
			correlations = append(correlations, fmt.Sprintf("Deployment revision %d changed %s before incident (image: %s -> %s)",
				dc.Revision, diff.Round(time.Second), dc.ImageBefore, dc.ImageAfter))
		}
	}
	for _, cc := range rca.RecentConfigChanges {
		diff := detectedAt.Sub(cc.Timestamp)
		if diff >= 0 && diff <= 10*time.Minute {
			correlations = append(correlations, fmt.Sprintf("ConfigMap %s changed %s before incident", cc.ConfigMapName, diff.Round(time.Second)))
		}
	}
	rca.TimeCorrelation = strings.Join(correlations, "; ")
}

func (e *RCAEnricher) rankCauses(rca *RCAContext) {
	if len(rca.RecentDeployments) > 0 {
		rca.PossibleCauses = append(rca.PossibleCauses, "Recent deployment change detected — possible bad release")
	}
	if len(rca.RecentConfigChanges) > 0 {
		rca.PossibleCauses = append(rca.PossibleCauses, "Recent configuration change — possible misconfiguration")
	}
	for _, dep := range rca.DependencyStatus {
		if !dep.Healthy {
			rca.PossibleCauses = append(rca.PossibleCauses, fmt.Sprintf("Upstream dependency %s is unhealthy — possible cascade failure", dep.ServiceName))
		}
	}
	if len(rca.RelatedIssues) > 0 {
		rca.PossibleCauses = append(rca.PossibleCauses, fmt.Sprintf("%d related active issues in same namespace — possible systemic problem", len(rca.RelatedIssues)))
	}
	if len(rca.PossibleCauses) == 0 {
		rca.PossibleCauses = append(rca.PossibleCauses, "No obvious external cause detected — may be resource exhaustion or application bug")
	}
}
