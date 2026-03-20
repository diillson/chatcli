package controllers

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// CascadeAnalyzer detects cascade failures across services by analyzing
// dependency graphs, temporal correlations, and cross-namespace issue patterns.
type CascadeAnalyzer struct {
	client client.Client
}

// CascadeResult contains the analysis of cascade failure patterns.
type CascadeResult struct {
	// Chain is the ordered list of services in the cascade failure chain.
	// First element is the suspected root cause service.
	Chain []CascadeNode
	// CrossNamespaceIssues are related issues in other namespaces.
	CrossNamespaceIssues []CrossNamespaceIssue
	// DependencyGraph maps service names to their discovered dependencies.
	DependencyGraph map[string][]ServiceDependency
	// RootCauseService is the suspected origin of the cascade.
	RootCauseService string
	// Summary is a human-readable summary.
	Summary string
}

// CascadeNode represents one service in a cascade chain.
type CascadeNode struct {
	ServiceName string
	Namespace   string
	IssueState  string
	Severity    string
	SignalType  string
	DetectedAt  time.Time
	// Position in the cascade: "root_cause", "intermediate", "victim"
	Role string
}

// CrossNamespaceIssue represents an active issue in another namespace.
type CrossNamespaceIssue struct {
	Name       string
	Namespace  string
	Severity   string
	SignalType string
	Resource   string
	State      string
	DetectedAt time.Time
}

// ServiceDependency represents a discovered dependency between services.
type ServiceDependency struct {
	ServiceName string
	Namespace   string
	Healthy     bool
	Endpoints   int
	Port        int32
}

// NewCascadeAnalyzer creates a new CascadeAnalyzer.
func NewCascadeAnalyzer(c client.Client) *CascadeAnalyzer {
	return &CascadeAnalyzer{client: c}
}

// AnalyzeCascade detects cascade failure patterns for the given issue.
func (ca *CascadeAnalyzer) AnalyzeCascade(ctx context.Context, issue *platformv1alpha1.Issue) (*CascadeResult, error) {
	result := &CascadeResult{
		DependencyGraph: make(map[string][]ServiceDependency),
	}

	resource := issue.Spec.Resource
	detectedAt := issue.CreationTimestamp.Time
	if issue.Status.DetectedAt != nil {
		detectedAt = issue.Status.DetectedAt.Time
	}

	// 1. Build dependency graph for the affected resource's namespace
	ca.buildDependencyGraph(ctx, resource.Namespace, result)

	// 2. Find temporally correlated issues (same namespace)
	sameNSIssues := ca.findCorrelatedIssues(ctx, issue, resource.Namespace, detectedAt)

	// 3. Find cross-namespace issues
	result.CrossNamespaceIssues = ca.findCrossNamespaceIssues(ctx, issue, detectedAt)

	// 4. Build cascade chain
	result.Chain = ca.buildCascadeChain(issue, sameNSIssues, result.DependencyGraph, detectedAt)

	// 5. Determine root cause service
	if len(result.Chain) > 0 {
		result.RootCauseService = result.Chain[0].ServiceName
	}

	// 6. Build summary
	result.Summary = ca.buildCascadeSummary(result)

	return result, nil
}

// buildDependencyGraph discovers service dependencies via EndpointSlices and Services.
func (ca *CascadeAnalyzer) buildDependencyGraph(ctx context.Context, namespace string, result *CascadeResult) {
	var services corev1.ServiceList
	if err := ca.client.List(ctx, &services, client.InNamespace(namespace)); err != nil {
		return
	}

	for _, svc := range services.Items {
		if svc.Spec.Type == corev1.ServiceTypeExternalName {
			continue
		}

		// Get endpoint health
		var epSlices discoveryv1.EndpointSliceList
		if err := ca.client.List(ctx, &epSlices, client.InNamespace(namespace),
			client.MatchingLabels{"kubernetes.io/service-name": svc.Name}); err != nil {
			continue
		}

		var readyCount int
		for _, eps := range epSlices.Items {
			for _, ep := range eps.Endpoints {
				if ep.Conditions.Ready != nil && *ep.Conditions.Ready {
					readyCount++
				}
			}
		}

		var port int32
		if len(svc.Spec.Ports) > 0 {
			port = svc.Spec.Ports[0].Port
		}

		dep := ServiceDependency{
			ServiceName: svc.Name,
			Namespace:   namespace,
			Healthy:     readyCount > 0,
			Endpoints:   readyCount,
			Port:        port,
		}

		result.DependencyGraph[svc.Name] = append(result.DependencyGraph[svc.Name], dep)
	}
}

// findCorrelatedIssues finds active issues in the same namespace within a time window.
func (ca *CascadeAnalyzer) findCorrelatedIssues(ctx context.Context, currentIssue *platformv1alpha1.Issue, namespace string, incidentTime time.Time) []platformv1alpha1.Issue {
	var issues platformv1alpha1.IssueList
	if err := ca.client.List(ctx, &issues, client.InNamespace(namespace)); err != nil {
		return nil
	}

	window := 15 * time.Minute
	var correlated []platformv1alpha1.Issue

	for _, iss := range issues.Items {
		if iss.Name == currentIssue.Name {
			continue
		}
		if isTerminalIssueState(iss.Status.State) && iss.Status.State != platformv1alpha1.IssueStateResolved {
			continue
		}

		issDetected := iss.CreationTimestamp.Time
		if iss.Status.DetectedAt != nil {
			issDetected = iss.Status.DetectedAt.Time
		}

		// Check temporal proximity
		diff := incidentTime.Sub(issDetected)
		if diff < 0 {
			diff = -diff
		}
		if diff <= window {
			correlated = append(correlated, iss)
		}
	}

	return correlated
}

// findCrossNamespaceIssues discovers active issues in other namespaces.
func (ca *CascadeAnalyzer) findCrossNamespaceIssues(ctx context.Context, currentIssue *platformv1alpha1.Issue, incidentTime time.Time) []CrossNamespaceIssue {
	var allIssues platformv1alpha1.IssueList
	if err := ca.client.List(ctx, &allIssues); err != nil {
		return nil
	}

	window := 20 * time.Minute
	var crossNS []CrossNamespaceIssue

	for _, iss := range allIssues.Items {
		if iss.Namespace == currentIssue.Namespace {
			continue
		}
		if isTerminalIssueState(iss.Status.State) {
			continue
		}

		issDetected := iss.CreationTimestamp.Time
		if iss.Status.DetectedAt != nil {
			issDetected = iss.Status.DetectedAt.Time
		}

		diff := incidentTime.Sub(issDetected)
		if diff < 0 {
			diff = -diff
		}
		if diff <= window {
			crossNS = append(crossNS, CrossNamespaceIssue{
				Name:       iss.Name,
				Namespace:  iss.Namespace,
				Severity:   string(iss.Spec.Severity),
				SignalType: iss.Spec.SignalType,
				Resource:   fmt.Sprintf("%s/%s", iss.Spec.Resource.Kind, iss.Spec.Resource.Name),
				State:      string(iss.Status.State),
				DetectedAt: issDetected,
			})
		}
	}

	// Sort by detected time
	sort.Slice(crossNS, func(i, j int) bool {
		return crossNS[i].DetectedAt.Before(crossNS[j].DetectedAt)
	})

	if len(crossNS) > 10 {
		crossNS = crossNS[:10]
	}

	return crossNS
}

// buildCascadeChain constructs the cascade failure chain ordered by time.
func (ca *CascadeAnalyzer) buildCascadeChain(currentIssue *platformv1alpha1.Issue, correlatedIssues []platformv1alpha1.Issue, deps map[string][]ServiceDependency, incidentTime time.Time) []CascadeNode {
	if len(correlatedIssues) == 0 {
		return nil
	}

	// Build nodes from all correlated issues + current
	var allNodes []CascadeNode

	for _, iss := range correlatedIssues {
		detected := iss.CreationTimestamp.Time
		if iss.Status.DetectedAt != nil {
			detected = iss.Status.DetectedAt.Time
		}
		allNodes = append(allNodes, CascadeNode{
			ServiceName: iss.Spec.Resource.Name,
			Namespace:   iss.Namespace,
			IssueState:  string(iss.Status.State),
			Severity:    string(iss.Spec.Severity),
			SignalType:  iss.Spec.SignalType,
			DetectedAt:  detected,
		})
	}

	detected := currentIssue.CreationTimestamp.Time
	if currentIssue.Status.DetectedAt != nil {
		detected = currentIssue.Status.DetectedAt.Time
	}
	allNodes = append(allNodes, CascadeNode{
		ServiceName: currentIssue.Spec.Resource.Name,
		Namespace:   currentIssue.Namespace,
		IssueState:  string(currentIssue.Status.State),
		Severity:    string(currentIssue.Spec.Severity),
		SignalType:  currentIssue.Spec.SignalType,
		DetectedAt:  detected,
	})

	// Sort by detection time — earliest first (likely root cause)
	sort.Slice(allNodes, func(i, j int) bool {
		return allNodes[i].DetectedAt.Before(allNodes[j].DetectedAt)
	})

	// Assign roles
	for i := range allNodes {
		switch {
		case i == 0:
			allNodes[i].Role = "root_cause"
		case i == len(allNodes)-1:
			allNodes[i].Role = "victim"
		default:
			allNodes[i].Role = "intermediate"
		}
	}

	// Check if dependencies support the cascade theory
	// (the root cause service should be a dependency of intermediate/victim services)
	if len(allNodes) >= 2 {
		rootSvc := allNodes[0].ServiceName
		isDependency := false
		for svc, depList := range deps {
			if svc == rootSvc {
				continue
			}
			for _, d := range depList {
				if d.ServiceName == rootSvc && !d.Healthy {
					isDependency = true
					break
				}
			}
		}
		if !isDependency {
			// No dependency evidence — downgrade from cascade to correlated
			for i := range allNodes {
				allNodes[i].Role = "correlated"
			}
		}
	}

	return allNodes
}

func (ca *CascadeAnalyzer) buildCascadeSummary(result *CascadeResult) string {
	var parts []string

	if len(result.Chain) >= 2 {
		var chain []string
		for _, n := range result.Chain {
			chain = append(chain, fmt.Sprintf("%s(%s)", n.ServiceName, n.Role))
		}
		parts = append(parts, fmt.Sprintf("Cascade chain: %s", strings.Join(chain, " → ")))
		if result.RootCauseService != "" {
			parts = append(parts, fmt.Sprintf("Suspected root cause: %s", result.RootCauseService))
		}
	}

	unhealthyDeps := 0
	for _, deps := range result.DependencyGraph {
		for _, d := range deps {
			if !d.Healthy {
				unhealthyDeps++
			}
		}
	}
	if unhealthyDeps > 0 {
		parts = append(parts, fmt.Sprintf("%d unhealthy service dependencies detected", unhealthyDeps))
	}

	if len(result.CrossNamespaceIssues) > 0 {
		parts = append(parts, fmt.Sprintf("%d active issues in other namespaces within time window", len(result.CrossNamespaceIssues)))
	}

	if len(parts) == 0 {
		return "No cascade failure patterns detected."
	}

	return strings.Join(parts, "; ")
}

// FormatForAI formats the cascade analysis for LLM consumption.
func (r *CascadeResult) FormatForAI() string {
	if r == nil {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## Cascade / Cross-Service Analysis\n\n")

	// Cascade chain
	if len(r.Chain) >= 2 {
		sb.WriteString("### Cascade Failure Chain (ordered by detection time)\n")
		for i, n := range r.Chain {
			arrow := ""
			if i < len(r.Chain)-1 {
				arrow = " →"
			}
			sb.WriteString(fmt.Sprintf("%d. **%s/%s** [%s] severity=%s signal=%s detected=%s%s\n",
				i+1, n.Namespace, n.ServiceName, n.Role, n.Severity, n.SignalType,
				n.DetectedAt.Format("15:04:05"), arrow))
		}
		if r.RootCauseService != "" {
			sb.WriteString(fmt.Sprintf("\n**Suspected root cause service: %s**\n", r.RootCauseService))
		}
		sb.WriteString("\n")
	}

	// Dependency graph - unhealthy services
	unhealthy := false
	for svc, deps := range r.DependencyGraph {
		for _, d := range deps {
			if !d.Healthy {
				if !unhealthy {
					sb.WriteString("### Unhealthy Service Dependencies\n")
					unhealthy = true
				}
				sb.WriteString(fmt.Sprintf("- %s → %s:%d (endpoints=%d, UNHEALTHY)\n",
					svc, d.ServiceName, d.Port, d.Endpoints))
			}
		}
	}
	if unhealthy {
		sb.WriteString("\n")
	}

	// Cross-namespace issues
	if len(r.CrossNamespaceIssues) > 0 {
		sb.WriteString("### Cross-Namespace Active Issues (within 20min window)\n")
		for _, ci := range r.CrossNamespaceIssues {
			sb.WriteString(fmt.Sprintf("- %s/%s [%s] %s signal=%s state=%s detected=%s\n",
				ci.Namespace, ci.Name, ci.Severity, ci.Resource, ci.SignalType,
				ci.State, ci.DetectedAt.Format("15:04:05")))
		}
		sb.WriteString("\n")
	}

	result := sb.String()
	if len(result) > 3000 {
		result = result[:2997] + "..."
	}
	return result
}
