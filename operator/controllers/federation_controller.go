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
	"k8s.io/apimachinery/pkg/api/meta"
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
			r.refreshClusterGauges(ctx, nil)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	log.Info("Reconciling ClusterRegistration", "name", cr.Name, "environment", cr.Spec.Environment)

	// 1. Read kubeconfig from referenced Secret
	remoteClient, err := r.getOrCreateRemoteClient(ctx, &cr)
	if err != nil {
		log.Error(err, "Failed to connect to remote cluster", "cluster", cr.Name)
		return r.markDisconnected(ctx, &cr, "KubeconfigUnusable", err)
	}

	// 2. Health check: list Nodes
	var nodeList corev1.NodeList
	if err := remoteClient.List(ctx, &nodeList); err != nil {
		log.Error(err, "Failed to list nodes on remote cluster", "cluster", cr.Name)
		r.remoteClients.Delete(cr.Name) // invalidate cached client
		return r.markDisconnected(ctx, &cr, "NodeListFailed", err)
	}

	// 3. Health check: list Namespaces
	var nsList corev1.NamespaceList
	if err := remoteClient.List(ctx, &nsList); err != nil {
		log.Error(err, "Failed to list namespaces on remote cluster", "cluster", cr.Name)
		r.remoteClients.Delete(cr.Name)
		return r.markDisconnected(ctx, &cr, "NamespaceListFailed", err)
	}

	// 4. Extract Kubernetes version from first node
	kubeVersion := ""
	if len(nodeList.Items) > 0 {
		kubeVersion = nodeList.Items[0].Status.NodeInfo.KubeletVersion
	}

	// 5. Count active Issues and RemediationPlans in the remote cluster (if CRDs exist)
	activeIssues, activeRemediations := countRemoteActivity(ctx, remoteClient)

	// 6. Update status
	now := metav1.Now()
	cr.Status.Connected = true
	cr.Status.LastHealthCheck = &now
	cr.Status.KubernetesVersion = kubeVersion
	cr.Status.NodeCount = clampInt32(len(nodeList.Items))
	cr.Status.NamespaceCount = clampInt32(len(nsList.Items))
	cr.Status.ActiveIssues = activeIssues
	cr.Status.ActiveRemediations = activeRemediations
	setClusterConditions(&cr, "HealthCheckPassed", "Nodes and namespaces listed", notReadyNodes(nodeList.Items))

	if err := r.Status().Update(ctx, &cr); err != nil {
		return ctrl.Result{}, err
	}
	r.refreshClusterGauges(ctx, &cr)

	log.Info("Cluster health check passed",
		"cluster", cr.Name,
		"nodes", cr.Status.NodeCount,
		"namespaces", cr.Status.NamespaceCount,
		"activeIssues", activeIssues,
		"activeRemediations", activeRemediations)

	return ctrl.Result{RequeueAfter: r.healthCheckInterval(&cr)}, nil
}

// markDisconnected records a failed health check on the registration.
func (r *FederationReconciler) markDisconnected(ctx context.Context, cr *platformv1alpha1.ClusterRegistration, reason string, cause error) (ctrl.Result, error) {
	cr.Status.Connected = false
	now := metav1.Now()
	cr.Status.LastHealthCheck = &now
	setClusterConditions(cr, reason, cause.Error(), 0)
	if statusErr := r.Status().Update(ctx, cr); statusErr != nil {
		return ctrl.Result{}, statusErr
	}
	r.refreshClusterGauges(ctx, cr)
	return ctrl.Result{RequeueAfter: r.healthCheckInterval(cr)}, nil
}

// countRemoteActivity counts the open Issues and running RemediationPlans of
// the remote cluster; a cluster without the CRDs counts zero.
func countRemoteActivity(ctx context.Context, remoteClient client.Client) (activeIssues, activeRemediations int32) {
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
	return activeIssues, activeRemediations
}

// Cluster health as the federation_clusters_total gauge, the REST API and
// the registration's conditions report it. The three states are exclusive.
const (
	ClusterStatusConnected    = "connected"
	ClusterStatusDegraded     = "degraded"
	ClusterStatusDisconnected = "disconnected"

	// ClusterConditionConnected is True while the health check passes.
	ClusterConditionConnected = "Connected"
	// ClusterConditionDegraded is True when the cluster answers but some of
	// its nodes are not Ready.
	ClusterConditionDegraded = "Degraded"
)

// notReadyNodes counts the nodes whose Ready condition is not True.
func notReadyNodes(nodes []corev1.Node) int {
	notReady := 0
	for _, node := range nodes {
		ready := false
		for _, cond := range node.Status.Conditions {
			if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
				ready = true
				break
			}
		}
		if !ready {
			notReady++
		}
	}
	return notReady
}

// setClusterConditions writes the Connected and Degraded conditions from
// the health check outcome.
func setClusterConditions(cr *platformv1alpha1.ClusterRegistration, reason, message string, notReady int) {
	connected := metav1.ConditionFalse
	if cr.Status.Connected {
		connected = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&cr.Status.Conditions, metav1.Condition{
		Type: ClusterConditionConnected, Status: connected, Reason: reason, Message: message,
		ObservedGeneration: cr.Generation,
	})
	degraded := metav1.Condition{
		Type: ClusterConditionDegraded, Status: metav1.ConditionFalse, Reason: "AllNodesReady",
		Message: "Every node is Ready", ObservedGeneration: cr.Generation,
	}
	switch {
	case !cr.Status.Connected:
		degraded.Status, degraded.Reason, degraded.Message = metav1.ConditionUnknown, "Disconnected", "The cluster is not reachable"
	case notReady > 0:
		degraded.Status, degraded.Reason = metav1.ConditionTrue, "NodesNotReady"
		degraded.Message = fmt.Sprintf("%d of %d nodes are not Ready", notReady, cr.Status.NodeCount)
	}
	meta.SetStatusCondition(&cr.Status.Conditions, degraded)
}

// ClusterHealth classifies a registration: disconnected when the last
// health check failed, degraded when it passed with nodes not Ready, and
// connected otherwise.
func ClusterHealth(cr *platformv1alpha1.ClusterRegistration) string {
	if !cr.Status.Connected {
		return ClusterStatusDisconnected
	}
	if meta.IsStatusConditionTrue(cr.Status.Conditions, ClusterConditionDegraded) {
		return ClusterStatusDegraded
	}
	return ClusterStatusConnected
}

// refreshClusterGauges sets federation_clusters_total for every state from
// the registrations that exist now, so a cluster that changes state or goes
// away moves between the series instead of being counted forever. current,
// when given, is the registration just written and wins over the cache.
func (r *FederationReconciler) refreshClusterGauges(ctx context.Context, current *platformv1alpha1.ClusterRegistration) {
	var list platformv1alpha1.ClusterRegistrationList
	if err := r.List(ctx, &list); err != nil {
		log.FromContext(ctx).Error(err, "Failed to list cluster registrations for the federation gauge")
		return
	}
	counts := map[string]float64{ClusterStatusConnected: 0, ClusterStatusDegraded: 0, ClusterStatusDisconnected: 0}
	seenCurrent := false
	for i := range list.Items {
		item := &list.Items[i]
		if current != nil && item.Namespace == current.Namespace && item.Name == current.Name {
			item = current
			seenCurrent = true
		}
		counts[ClusterHealth(item)]++
	}
	if current != nil && !seenCurrent {
		counts[ClusterHealth(current)]++
	}
	for status, n := range counts {
		federationClustersTotal.WithLabelValues(status).Set(n)
	}
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

// Annotations CorrelateAcrossClusters writes on every correlated Issue, and
// GET /api/v1/federation/correlations reads.
const (
	// AnnotationCrossClusterCorrelation is the correlation ID shared by the group.
	AnnotationCrossClusterCorrelation = "platform.chatcli.io/cross-cluster-correlation"
	// AnnotationAffectedClusters is how many clusters the group spans.
	AnnotationAffectedClusters = "platform.chatcli.io/affected-clusters"
	// AnnotationSeverityElevated is "true" when correlation raised the
	// Issue's severity to critical.
	AnnotationSeverityElevated = "platform.chatcli.io/elevated-severity"
)

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

	// One correlation keeps one ID: a new matching Issue joins the group an
	// earlier Issue already carries instead of relabeling it.
	correlationID := ""
	for _, mi := range matchingIssues {
		if id := mi.issue.Annotations[AnnotationCrossClusterCorrelation]; id != "" {
			correlationID = id
			break
		}
	}
	if correlationID == "" {
		correlationID = fmt.Sprintf("xcluster-%s", uuid.New().String()[:8])
		federationCrossClusterIssuesTotal.Inc()
	}

	// Annotate all matching issues
	for _, mi := range matchingIssues {
		issueCopy := mi.issue.DeepCopy()
		if issueCopy.Annotations == nil {
			issueCopy.Annotations = make(map[string]string)
		}
		issueCopy.Annotations[AnnotationCrossClusterCorrelation] = correlationID
		issueCopy.Annotations[AnnotationAffectedClusters] = fmt.Sprintf("%d", len(clusterSet))

		// Elevate severity to critical if not already
		if issueCopy.Spec.Severity != platformv1alpha1.IssueSeverityCritical {
			issueCopy.Spec.Severity = platformv1alpha1.IssueSeverityCritical
			issueCopy.Annotations[AnnotationSeverityElevated] = "true"
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
