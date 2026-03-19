package controllers

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

var (
	federationClustersTotal = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "chatcli",
		Subsystem: "operator",
		Name:      "federation_clusters_total",
		Help:      "Total number of federated clusters by connection status.",
	}, []string{"status"})

	federationCrossClusterIssuesTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "chatcli",
		Subsystem: "operator",
		Name:      "federation_cross_cluster_issues_total",
		Help:      "Total cross-cluster correlated issues detected.",
	})

	federationCascadeDetectedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "chatcli",
		Subsystem: "operator",
		Name:      "federation_cascade_detected_total",
		Help:      "Total cascade failures detected across environments.",
	})
)

func init() {
	ctrlmetrics.Registry.MustRegister(
		federationClustersTotal,
		federationCrossClusterIssuesTotal,
		federationCascadeDetectedTotal,
	)
}

// FederationReconciler watches ClusterRegistration CRs and manages remote cluster connectivity.
type FederationReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	remoteClients sync.Map // name -> client.Client
}

// GlobalStatus aggregates status across all federated clusters.
type GlobalStatus struct {
	TotalClusters      int
	ConnectedClusters  int
	TotalActiveIssues  int
	IssuesBySeverity   map[string]int
	ActiveRemediations int
	UnhealthyClusters  []string
}

// +kubebuilder:rbac:groups=platform.chatcli.io,resources=clusterregistrations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=clusterregistrations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=issues,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=remediationplans,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list

func (r *FederationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var cr platformv1alpha1.ClusterRegistration
	if err := r.Get(ctx, req.NamespacedName, &cr); err != nil {
		if errors.IsNotFound(err) {
			// Cluster removed: clean up cached client
			r.remoteClients.Delete(req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	log.Info("Reconciling ClusterRegistration", "name", cr.Name, "environment", cr.Spec.Environment)

	// 1. Read kubeconfig from referenced Secret
	remoteClient, err := r.getOrCreateRemoteClient(ctx, &cr)
	if err != nil {
		log.Error(err, "Failed to connect to remote cluster", "cluster", cr.Name)
		cr.Status.Connected = false
		now := metav1.Now()
		cr.Status.LastHealthCheck = &now
		if statusErr := r.Status().Update(ctx, &cr); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		federationClustersTotal.WithLabelValues("disconnected").Inc()
		return ctrl.Result{RequeueAfter: r.healthCheckInterval(&cr)}, nil
	}

	// 2. Health check: list Nodes
	var nodeList corev1.NodeList
	if err := remoteClient.List(ctx, &nodeList); err != nil {
		log.Error(err, "Failed to list nodes on remote cluster", "cluster", cr.Name)
		cr.Status.Connected = false
		now := metav1.Now()
		cr.Status.LastHealthCheck = &now
		r.remoteClients.Delete(cr.Name) // invalidate cached client
		if statusErr := r.Status().Update(ctx, &cr); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		federationClustersTotal.WithLabelValues("disconnected").Inc()
		return ctrl.Result{RequeueAfter: r.healthCheckInterval(&cr)}, nil
	}

	// 3. Health check: list Namespaces
	var nsList corev1.NamespaceList
	if err := remoteClient.List(ctx, &nsList); err != nil {
		log.Error(err, "Failed to list namespaces on remote cluster", "cluster", cr.Name)
		cr.Status.Connected = false
		now := metav1.Now()
		cr.Status.LastHealthCheck = &now
		r.remoteClients.Delete(cr.Name)
		if statusErr := r.Status().Update(ctx, &cr); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{RequeueAfter: r.healthCheckInterval(&cr)}, nil
	}

	// 4. Extract Kubernetes version from first node
	kubeVersion := ""
	if len(nodeList.Items) > 0 {
		kubeVersion = nodeList.Items[0].Status.NodeInfo.KubeletVersion
	}

	// 5. Count active Issues and RemediationPlans in the remote cluster (if CRDs exist)
	activeIssues := int32(0)
	activeRemediations := int32(0)

	var issueList platformv1alpha1.IssueList
	if err := remoteClient.List(ctx, &issueList); err == nil {
		for _, issue := range issueList.Items {
			if !isTerminalIssueState(issue.Status.State) {
				activeIssues++
			}
		}
	}

	var planList platformv1alpha1.RemediationPlanList
	if err := remoteClient.List(ctx, &planList); err == nil {
		for _, plan := range planList.Items {
			switch plan.Status.State {
			case platformv1alpha1.RemediationStatePending,
				platformv1alpha1.RemediationStateExecuting,
				platformv1alpha1.RemediationStateVerifying:
				activeRemediations++
			}
		}
	}

	// 6. Update status
	now := metav1.Now()
	cr.Status.Connected = true
	cr.Status.LastHealthCheck = &now
	cr.Status.KubernetesVersion = kubeVersion
	cr.Status.NodeCount = int32(len(nodeList.Items))
	cr.Status.NamespaceCount = int32(len(nsList.Items))
	cr.Status.ActiveIssues = activeIssues
	cr.Status.ActiveRemediations = activeRemediations

	if err := r.Status().Update(ctx, &cr); err != nil {
		return ctrl.Result{}, err
	}

	federationClustersTotal.WithLabelValues("connected").Inc()

	log.Info("Cluster health check passed",
		"cluster", cr.Name,
		"nodes", cr.Status.NodeCount,
		"namespaces", cr.Status.NamespaceCount,
		"activeIssues", activeIssues,
		"activeRemediations", activeRemediations)

	return ctrl.Result{RequeueAfter: r.healthCheckInterval(&cr)}, nil
}

// getOrCreateRemoteClient reads the kubeconfig Secret and creates or retrieves a cached remote client.
func (r *FederationReconciler) getOrCreateRemoteClient(ctx context.Context, cr *platformv1alpha1.ClusterRegistration) (client.Client, error) {
	// Check cache first
	if cached, ok := r.remoteClients.Load(cr.Name); ok {
		return cached.(client.Client), nil
	}

	// Read Secret containing kubeconfig
	var secret corev1.Secret
	secretRef := types.NamespacedName{
		Name:      cr.Spec.KubeconfigSecretRef.Name,
		Namespace: cr.Namespace,
	}
	if err := r.Get(ctx, secretRef, &secret); err != nil {
		return nil, fmt.Errorf("failed to get kubeconfig secret %s: %w", secretRef, err)
	}

	kubeconfigBytes, ok := secret.Data["kubeconfig"]
	if !ok {
		return nil, fmt.Errorf("secret %s does not contain 'kubeconfig' key", secretRef)
	}

	// Create REST config from kubeconfig data
	restConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse kubeconfig: %w", err)
	}

	// Build a controller-runtime client for the remote cluster
	scheme := r.Scheme
	remoteClient, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("failed to create remote client: %w", err)
	}

	// Cache the client
	r.remoteClients.Store(cr.Name, remoteClient)
	return remoteClient, nil
}

// healthCheckInterval returns the health check interval from the ClusterRegistration spec.
func (r *FederationReconciler) healthCheckInterval(cr *platformv1alpha1.ClusterRegistration) time.Duration {
	if cr.Spec.HealthCheckInterval != "" {
		d, err := time.ParseDuration(cr.Spec.HealthCheckInterval)
		if err == nil {
			return d
		}
	}
	return 30 * time.Second
}

// CorrelateAcrossClusters checks if the same issue type exists across multiple clusters.
// If the same SignalType appears in 3+ clusters, all matching issues are annotated with
// a correlation ID and severity may be elevated to critical.
func (r *FederationReconciler) CorrelateAcrossClusters(ctx context.Context, issue *platformv1alpha1.Issue) error {
	log := log.FromContext(ctx)

	if issue.Spec.SignalType == "" {
		return nil
	}

	// Collect matching issues from all connected clusters
	type clusterIssue struct {
		clusterName string
		issue       platformv1alpha1.Issue
	}
	var matchingIssues []clusterIssue

	// Include issues from the local cluster
	var localIssues platformv1alpha1.IssueList
	if err := r.List(ctx, &localIssues); err == nil {
		for _, iss := range localIssues.Items {
			if iss.Spec.SignalType == issue.Spec.SignalType && !isTerminalIssueState(iss.Status.State) {
				matchingIssues = append(matchingIssues, clusterIssue{
					clusterName: "local",
					issue:       iss,
				})
			}
		}
	}

	// Query each connected remote cluster
	var clusterList platformv1alpha1.ClusterRegistrationList
	if err := r.List(ctx, &clusterList); err != nil {
		return fmt.Errorf("listing cluster registrations: %w", err)
	}

	for _, cr := range clusterList.Items {
		if !cr.Status.Connected {
			continue
		}

		cached, ok := r.remoteClients.Load(cr.Name)
		if !ok {
			continue
		}
		remoteClient := cached.(client.Client)

		var remoteIssues platformv1alpha1.IssueList
		if err := remoteClient.List(ctx, &remoteIssues); err != nil {
			log.Info("Failed to list issues on remote cluster", "cluster", cr.Name, "error", err)
			continue
		}

		for _, iss := range remoteIssues.Items {
			if iss.Spec.SignalType == issue.Spec.SignalType && !isTerminalIssueState(iss.Status.State) {
				matchingIssues = append(matchingIssues, clusterIssue{
					clusterName: cr.Name,
					issue:       iss,
				})
			}
		}
	}

	// If same issue type exists in 3+ clusters, correlate
	clusterSet := make(map[string]struct{})
	for _, mi := range matchingIssues {
		clusterSet[mi.clusterName] = struct{}{}
	}

	if len(clusterSet) < 3 {
		return nil
	}

	log.Info("Cross-cluster correlation detected",
		"signalType", issue.Spec.SignalType,
		"clusterCount", len(clusterSet))

	correlationID := fmt.Sprintf("xcluster-%s", uuid.New().String()[:8])
	federationCrossClusterIssuesTotal.Inc()

	// Annotate all matching issues
	for _, mi := range matchingIssues {
		issueCopy := mi.issue.DeepCopy()
		if issueCopy.Annotations == nil {
			issueCopy.Annotations = make(map[string]string)
		}
		issueCopy.Annotations["platform.chatcli.io/cross-cluster-correlation"] = correlationID
		issueCopy.Annotations["platform.chatcli.io/affected-clusters"] = fmt.Sprintf("%d", len(clusterSet))

		// Elevate severity to critical if not already
		if issueCopy.Spec.Severity != platformv1alpha1.IssueSeverityCritical {
			issueCopy.Spec.Severity = platformv1alpha1.IssueSeverityCritical
		}

		// Update on the appropriate client
		if mi.clusterName == "local" {
			if err := r.Update(ctx, issueCopy); err != nil && !errors.IsConflict(err) {
				log.Info("Failed to annotate local issue", "issue", issueCopy.Name, "error", err)
			}
		} else {
			if cached, ok := r.remoteClients.Load(mi.clusterName); ok {
				remoteClient := cached.(client.Client)
				if err := remoteClient.Update(ctx, issueCopy); err != nil && !errors.IsConflict(err) {
					log.Info("Failed to annotate remote issue", "cluster", mi.clusterName, "issue", issueCopy.Name, "error", err)
				}
			}
		}
	}

	return nil
}

// DetectCascade checks if the same resource name had issues in staging before production.
// If yes, the issue is annotated as a cascade failure.
func (r *FederationReconciler) DetectCascade(ctx context.Context, issue *platformv1alpha1.Issue) (bool, error) {
	log := log.FromContext(ctx)

	// Only check for prod issues
	var clusterList platformv1alpha1.ClusterRegistrationList
	if err := r.List(ctx, &clusterList); err != nil {
		return false, fmt.Errorf("listing cluster registrations: %w", err)
	}

	// Identify staging and prod clusters
	var stagingClusters []platformv1alpha1.ClusterRegistration
	var prodClusters []platformv1alpha1.ClusterRegistration
	for _, cr := range clusterList.Items {
		if !cr.Status.Connected {
			continue
		}
		switch cr.Spec.Environment {
		case "staging":
			stagingClusters = append(stagingClusters, cr)
		case "prod":
			prodClusters = append(prodClusters, cr)
		}
	}

	if len(stagingClusters) == 0 || len(prodClusters) == 0 {
		return false, nil
	}

	resourceName := issue.Spec.Resource.Name

	// Check staging clusters for issues with the same resource name
	for _, staging := range stagingClusters {
		cached, ok := r.remoteClients.Load(staging.Name)
		if !ok {
			continue
		}
		remoteClient := cached.(client.Client)

		var stagingIssues platformv1alpha1.IssueList
		if err := remoteClient.List(ctx, &stagingIssues); err != nil {
			log.Info("Failed to list issues on staging cluster", "cluster", staging.Name, "error", err)
			continue
		}

		for _, stagingIssue := range stagingIssues.Items {
			if stagingIssue.Spec.Resource.Name != resourceName {
				continue
			}
			// Found same resource name in staging with an issue
			// Check if the staging issue was detected before the prod issue
			if stagingIssue.Status.DetectedAt != nil && issue.Status.DetectedAt != nil {
				if stagingIssue.Status.DetectedAt.Time.Before(issue.Status.DetectedAt.Time) {
					log.Info("Cascade failure detected",
						"resource", resourceName,
						"stagingCluster", staging.Name,
						"stagingIssue", stagingIssue.Name,
						"prodIssue", issue.Name)

					// Annotate the prod issue
					if issue.Annotations == nil {
						issue.Annotations = make(map[string]string)
					}
					issue.Annotations["platform.chatcli.io/cascade-detected"] = "true"
					issue.Annotations["platform.chatcli.io/cascade-source-cluster"] = staging.Name
					issue.Annotations["platform.chatcli.io/cascade-source-issue"] = stagingIssue.Name

					if err := r.Update(ctx, issue); err != nil {
						return true, fmt.Errorf("updating issue with cascade annotation: %w", err)
					}

					federationCascadeDetectedTotal.Inc()
					return true, nil
				}
			}
		}
	}

	return false, nil
}

// GetGlobalStatus aggregates status from all registered clusters.
func (r *FederationReconciler) GetGlobalStatus(ctx context.Context) (*GlobalStatus, error) {
	var clusterList platformv1alpha1.ClusterRegistrationList
	if err := r.List(ctx, &clusterList); err != nil {
		return nil, fmt.Errorf("listing cluster registrations: %w", err)
	}

	status := &GlobalStatus{
		IssuesBySeverity: make(map[string]int),
	}

	for _, cr := range clusterList.Items {
		status.TotalClusters++

		if cr.Status.Connected {
			status.ConnectedClusters++
		} else {
			status.UnhealthyClusters = append(status.UnhealthyClusters, cr.Name)
		}

		status.TotalActiveIssues += int(cr.Status.ActiveIssues)
		status.ActiveRemediations += int(cr.Status.ActiveRemediations)

		// Count issues by severity from connected clusters
		if cr.Status.Connected {
			cached, ok := r.remoteClients.Load(cr.Name)
			if !ok {
				continue
			}
			remoteClient := cached.(client.Client)

			var issues platformv1alpha1.IssueList
			if err := remoteClient.List(ctx, &issues); err != nil {
				continue
			}
			for _, issue := range issues.Items {
				if !isTerminalIssueState(issue.Status.State) {
					status.IssuesBySeverity[string(issue.Spec.Severity)]++
				}
			}
		}
	}

	// Also include local cluster issues
	var localIssues platformv1alpha1.IssueList
	if err := r.List(ctx, &localIssues); err == nil {
		for _, issue := range localIssues.Items {
			if !isTerminalIssueState(issue.Status.State) {
				status.IssuesBySeverity[string(issue.Spec.Severity)]++
			}
		}
	}

	return status, nil
}

// GetClusterApprovalMode determines the remediation approval mode based on cluster tier and issue severity.
func (r *FederationReconciler) GetClusterApprovalMode(ctx context.Context, clusterName string) (string, error) {
	var clusterList platformv1alpha1.ClusterRegistrationList
	if err := r.List(ctx, &clusterList); err != nil {
		return "manual", fmt.Errorf("listing cluster registrations: %w", err)
	}

	var targetCluster *platformv1alpha1.ClusterRegistration
	for i := range clusterList.Items {
		if clusterList.Items[i].Name == clusterName ||
			clusterList.Items[i].Spec.DisplayName == clusterName {
			targetCluster = &clusterList.Items[i]
			break
		}
	}

	if targetCluster == nil {
		return "manual", fmt.Errorf("cluster %q not found", clusterName)
	}

	tier := strings.ToLower(targetCluster.Spec.Tier)

	switch tier {
	case "critical":
		// critical tier: manual for all severities
		return "manual", nil
	case "standard":
		// standard tier: auto for medium/low, manual for critical/high
		return "auto-medium-low", nil
	case "non-critical":
		// non-critical tier: auto for all
		return "auto", nil
	default:
		return "manual", nil
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *FederationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.ClusterRegistration{}).
		Complete(r)
}
