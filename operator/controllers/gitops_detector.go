package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// GitOpsDetector detects and interacts with Helm releases, ArgoCD Applications,
// and Flux Kustomizations to provide deployment context and enable GitOps-aware remediation.
type GitOpsDetector struct {
	client client.Client
}

// HelmReleaseInfo contains parsed information about a Helm release.
type HelmReleaseInfo struct {
	Name       string
	Namespace  string
	Chart      string
	Version    string
	AppVersion string
	Status     string // deployed, failed, pending-install, pending-upgrade, uninstalling
	Revision   int
	UpdatedAt  time.Time
	Values     map[string]interface{}
	// Previous release for rollback reference
	PreviousRevision int
	PreviousStatus   string
}

// ArgoCDAppInfo contains parsed information about an ArgoCD Application.
type ArgoCDAppInfo struct {
	Name          string
	Namespace     string
	Project       string
	RepoURL       string
	Path          string
	TargetRevision string
	SyncStatus    string // Synced, OutOfSync, Unknown
	HealthStatus  string // Healthy, Degraded, Progressing, Missing, Suspended, Unknown
	SyncPolicy    string // manual, automated
	LastSyncedAt  time.Time
	LastSyncResult string
	Conditions    []string
}

// FluxKustomizationInfo contains parsed information about a Flux Kustomization.
type FluxKustomizationInfo struct {
	Name        string
	Namespace   string
	SourceRef   string
	Path        string
	Ready       bool
	Suspended   bool
	LastApplied time.Time
	Conditions  []string
}

// GitOpsContext aggregates all GitOps state relevant to an incident.
type GitOpsContext struct {
	HelmRelease  *HelmReleaseInfo
	ArgoCDApp    *ArgoCDAppInfo
	FluxResource *FluxKustomizationInfo
	Summary      string
}

// NewGitOpsDetector creates a new GitOpsDetector.
func NewGitOpsDetector(c client.Client) *GitOpsDetector {
	return &GitOpsDetector{client: c}
}

// DetectGitOpsContext discovers Helm, ArgoCD, and Flux resources related to the given workload.
func (g *GitOpsDetector) DetectGitOpsContext(ctx context.Context, resource platformv1alpha1.ResourceRef) (*GitOpsContext, error) {
	result := &GitOpsContext{}

	// 1. Detect Helm release (stored as Kubernetes Secrets with type helm.sh/release.v1)
	helmRelease, err := g.detectHelmRelease(ctx, resource)
	if err == nil && helmRelease != nil {
		result.HelmRelease = helmRelease
	}

	// 2. Detect ArgoCD Application
	argoApp, err := g.detectArgoCDApp(ctx, resource)
	if err == nil && argoApp != nil {
		result.ArgoCDApp = argoApp
	}

	// 3. Detect Flux Kustomization
	fluxResource, err := g.detectFluxKustomization(ctx, resource)
	if err == nil && fluxResource != nil {
		result.FluxResource = fluxResource
	}

	result.Summary = g.buildGitOpsSummary(result)
	return result, nil
}

// detectHelmRelease finds Helm release information from Kubernetes Secrets.
// Helm 3 stores release data in Secrets with type "helm.sh/release.v1".
func (g *GitOpsDetector) detectHelmRelease(ctx context.Context, resource platformv1alpha1.ResourceRef) (*HelmReleaseInfo, error) {
	var secrets corev1.SecretList
	if err := g.client.List(ctx, &secrets, client.InNamespace(resource.Namespace),
		client.MatchingLabels{"owner": "helm"}); err != nil {
		return nil, err
	}

	type releaseEntry struct {
		name     string
		revision int
		secret   corev1.Secret
	}

	// Find secrets matching the resource name
	var candidates []releaseEntry
	for i := range secrets.Items {
		s := &secrets.Items[i]
		releaseName := s.Labels["name"]
		if releaseName == "" {
			continue
		}

		// Match: release name equals resource name or resource labels indicate helm release
		if releaseName != resource.Name {
			continue
		}

		rev := 0
		if revStr, ok := s.Labels["version"]; ok {
			fmt.Sscanf(revStr, "%d", &rev)
		}

		candidates = append(candidates, releaseEntry{
			name: releaseName, revision: rev, secret: *s,
		})
	}

	if len(candidates) == 0 {
		return nil, nil
	}

	// Sort by revision descending — latest first
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].revision > candidates[j].revision
	})

	latest := candidates[0]
	info := &HelmReleaseInfo{
		Name:      latest.name,
		Namespace: resource.Namespace,
		Revision:  latest.revision,
		Status:    latest.secret.Labels["status"],
	}

	// Parse release data if available
	if releaseData, ok := latest.secret.Data["release"]; ok {
		g.parseHelmReleaseData(releaseData, info)
	}

	// Get previous revision for rollback reference
	if len(candidates) > 1 {
		info.PreviousRevision = candidates[1].revision
		info.PreviousStatus = candidates[1].secret.Labels["status"]
	}

	return info, nil
}

// parseHelmReleaseData extracts chart info from the Helm release secret data.
// Helm stores this as base64-encoded, gzipped, JSON data.
func (g *GitOpsDetector) parseHelmReleaseData(data []byte, info *HelmReleaseInfo) {
	// Helm release data is base64 -> gzip -> json. The secret data is already base64-decoded
	// by Kubernetes. We try to parse as JSON first (some Helm versions store plain JSON).
	var release struct {
		Chart struct {
			Metadata struct {
				Name       string `json:"name"`
				Version    string `json:"version"`
				AppVersion string `json:"appVersion"`
			} `json:"metadata"`
		} `json:"chart"`
		Info struct {
			Status      string `json:"status"`
			Description string `json:"description"`
			FirstDeployed time.Time `json:"first_deployed"`
			LastDeployed  time.Time `json:"last_deployed"`
		} `json:"info"`
		Config map[string]interface{} `json:"config"`
	}

	// Try direct JSON parse (works in some setups)
	if err := json.Unmarshal(data, &release); err == nil {
		if release.Chart.Metadata.Name != "" {
			info.Chart = release.Chart.Metadata.Name
			info.Version = release.Chart.Metadata.Version
			info.AppVersion = release.Chart.Metadata.AppVersion
		}
		if !release.Info.LastDeployed.IsZero() {
			info.UpdatedAt = release.Info.LastDeployed
		}
		if release.Info.Status != "" {
			info.Status = release.Info.Status
		}
		info.Values = release.Config
	}
	// If JSON parse fails, the data is likely gzipped — we still have the labels-based info
}

// detectArgoCDApp finds ArgoCD Application CRs managing this resource.
func (g *GitOpsDetector) detectArgoCDApp(ctx context.Context, resource platformv1alpha1.ResourceRef) (*ArgoCDAppInfo, error) {
	// ArgoCD Application GVR
	argoGVR := schema.GroupVersionResource{
		Group:    "argoproj.io",
		Version:  "v1alpha1",
		Resource: "applications",
	}

	// List ArgoCD Applications using unstructured client
	appList := &unstructured.UnstructuredList{}
	appList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   argoGVR.Group,
		Version: argoGVR.Version,
		Kind:    "ApplicationList",
	})

	// Try argocd namespace first, then the resource namespace
	for _, ns := range []string{"argocd", "argo-cd", resource.Namespace} {
		if err := g.client.List(ctx, appList, client.InNamespace(ns)); err != nil {
			continue
		}
		for _, app := range appList.Items {
			if appManagesResource(&app, resource) {
				return parseArgoCDApp(&app), nil
			}
		}
	}

	return nil, nil
}

// appManagesResource checks if an ArgoCD Application manages the given resource.
func appManagesResource(app *unstructured.Unstructured, resource platformv1alpha1.ResourceRef) bool {
	// Check spec.destination.namespace
	destNS, _, _ := unstructured.NestedString(app.Object, "spec", "destination", "namespace")
	if destNS != "" && destNS != resource.Namespace {
		return false
	}

	// Check if app name matches resource name (common pattern)
	if app.GetName() == resource.Name {
		return true
	}

	// Check status.resources for the specific resource
	resources, found, _ := unstructured.NestedSlice(app.Object, "status", "resources")
	if found {
		for _, r := range resources {
			if rMap, ok := r.(map[string]interface{}); ok {
				rName, _ := rMap["name"].(string)
				rKind, _ := rMap["kind"].(string)
				rNS, _ := rMap["namespace"].(string)
				if rName == resource.Name && rKind == resource.Kind &&
					(rNS == resource.Namespace || rNS == "") {
					return true
				}
			}
		}
	}

	return false
}

// parseArgoCDApp extracts relevant fields from an ArgoCD Application unstructured object.
func parseArgoCDApp(app *unstructured.Unstructured) *ArgoCDAppInfo {
	info := &ArgoCDAppInfo{
		Name:      app.GetName(),
		Namespace: app.GetNamespace(),
	}

	info.Project, _, _ = unstructured.NestedString(app.Object, "spec", "project")
	info.RepoURL, _, _ = unstructured.NestedString(app.Object, "spec", "source", "repoURL")
	info.Path, _, _ = unstructured.NestedString(app.Object, "spec", "source", "path")
	info.TargetRevision, _, _ = unstructured.NestedString(app.Object, "spec", "source", "targetRevision")

	// Sync status
	info.SyncStatus, _, _ = unstructured.NestedString(app.Object, "status", "sync", "status")
	info.HealthStatus, _, _ = unstructured.NestedString(app.Object, "status", "health", "status")

	// Sync policy
	autoSync, found, _ := unstructured.NestedMap(app.Object, "spec", "syncPolicy", "automated")
	if found && autoSync != nil {
		info.SyncPolicy = "automated"
	} else {
		info.SyncPolicy = "manual"
	}

	// Last sync
	lastSync, found, _ := unstructured.NestedString(app.Object, "status", "operationState", "finishedAt")
	if found && lastSync != "" {
		if t, err := time.Parse(time.RFC3339, lastSync); err == nil {
			info.LastSyncedAt = t
		}
	}

	info.LastSyncResult, _, _ = unstructured.NestedString(app.Object, "status", "operationState", "phase")

	// Conditions
	conditions, found, _ := unstructured.NestedSlice(app.Object, "status", "conditions")
	if found {
		for _, c := range conditions {
			if cMap, ok := c.(map[string]interface{}); ok {
				cType, _ := cMap["type"].(string)
				cMsg, _ := cMap["message"].(string)
				info.Conditions = append(info.Conditions, fmt.Sprintf("%s: %s", cType, cMsg))
			}
		}
	}

	return info
}

// detectFluxKustomization finds Flux Kustomization CRs managing this resource.
func (g *GitOpsDetector) detectFluxKustomization(ctx context.Context, resource platformv1alpha1.ResourceRef) (*FluxKustomizationInfo, error) {
	kustomizationList := &unstructured.UnstructuredList{}
	kustomizationList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "kustomize.toolkit.fluxcd.io",
		Version: "v1",
		Kind:    "KustomizationList",
	})

	// Try flux-system namespace and resource namespace
	for _, ns := range []string{"flux-system", resource.Namespace} {
		if err := g.client.List(ctx, kustomizationList, client.InNamespace(ns)); err != nil {
			continue
		}
		for _, k := range kustomizationList.Items {
			if fluxManagesNamespace(&k, resource.Namespace) || k.GetName() == resource.Name {
				return parseFluxKustomization(&k), nil
			}
		}
	}

	return nil, nil
}

func fluxManagesNamespace(k *unstructured.Unstructured, namespace string) bool {
	targetNS, _, _ := unstructured.NestedString(k.Object, "spec", "targetNamespace")
	return targetNS == namespace
}

func parseFluxKustomization(k *unstructured.Unstructured) *FluxKustomizationInfo {
	info := &FluxKustomizationInfo{
		Name:      k.GetName(),
		Namespace: k.GetNamespace(),
	}

	info.Path, _, _ = unstructured.NestedString(k.Object, "spec", "path")
	info.Suspended, _, _ = unstructured.NestedBool(k.Object, "spec", "suspend")

	sourceKind, _, _ := unstructured.NestedString(k.Object, "spec", "sourceRef", "kind")
	sourceName, _, _ := unstructured.NestedString(k.Object, "spec", "sourceRef", "name")
	info.SourceRef = fmt.Sprintf("%s/%s", sourceKind, sourceName)

	// Parse conditions
	conditions, found, _ := unstructured.NestedSlice(k.Object, "status", "conditions")
	if found {
		for _, c := range conditions {
			if cMap, ok := c.(map[string]interface{}); ok {
				cType, _ := cMap["type"].(string)
				cStatus, _ := cMap["status"].(string)
				cMsg, _ := cMap["message"].(string)

				if cType == "Ready" && cStatus == "True" {
					info.Ready = true
				}
				if cStatus != "True" || cType != "Ready" {
					info.Conditions = append(info.Conditions, fmt.Sprintf("%s=%s: %s", cType, cStatus, cMsg))
				}

				if cType == "Ready" {
					if ts, _ := cMap["lastTransitionTime"].(string); ts != "" {
						if t, err := time.Parse(time.RFC3339, ts); err == nil {
							info.LastApplied = t
						}
					}
				}
			}
		}
	}

	return info
}

// ExecuteHelmRollback performs a Helm rollback by patching the release secret.
// This creates a new release secret with the previous revision's configuration.
func (g *GitOpsDetector) ExecuteHelmRollback(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	release, err := g.detectHelmRelease(ctx, resource)
	if err != nil {
		return fmt.Errorf("detecting helm release: %w", err)
	}
	if release == nil {
		return fmt.Errorf("no helm release found for %s/%s", resource.Namespace, resource.Name)
	}

	if release.PreviousRevision == 0 {
		return fmt.Errorf("helm release %s has no previous revision to roll back to", release.Name)
	}

	targetRevision := release.PreviousRevision
	if rev, ok := params["revision"]; ok {
		fmt.Sscanf(rev, "%d", &targetRevision)
	}

	// Find the target revision secret
	var secrets corev1.SecretList
	if err := g.client.List(ctx, &secrets, client.InNamespace(resource.Namespace),
		client.MatchingLabels{
			"owner":   "helm",
			"name":    release.Name,
			"version": fmt.Sprintf("%d", targetRevision),
		}); err != nil {
		return fmt.Errorf("finding target revision secret: %w", err)
	}

	if len(secrets.Items) == 0 {
		return fmt.Errorf("helm release revision %d not found for %s", targetRevision, release.Name)
	}

	targetSecret := &secrets.Items[0]

	// Create a new release secret (next revision) with the target's release data
	newRevision := release.Revision + 1
	newSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("sh.helm.release.v1.%s.v%d", release.Name, newRevision),
			Namespace: resource.Namespace,
			Labels: map[string]string{
				"owner":            "helm",
				"name":             release.Name,
				"version":          fmt.Sprintf("%d", newRevision),
				"status":           "deployed",
				"modifiedAt":       fmt.Sprintf("%d", time.Now().Unix()),
			},
		},
		Type: corev1.SecretType("helm.sh/release.v1"),
		Data: targetSecret.Data,
	}

	if err := g.client.Create(ctx, newSecret); err != nil {
		return fmt.Errorf("creating rollback release secret: %w", err)
	}

	// Mark the current release as superseded
	currentSecretName := fmt.Sprintf("sh.helm.release.v1.%s.v%d", release.Name, release.Revision)
	var currentSecret corev1.Secret
	if err := g.client.Get(ctx, types.NamespacedName{
		Name: currentSecretName, Namespace: resource.Namespace,
	}, &currentSecret); err == nil {
		currentSecret.Labels["status"] = "superseded"
		_ = g.client.Update(ctx, &currentSecret)
	}

	return nil
}

// ExecuteArgoSync triggers a sync on an ArgoCD Application.
func (g *GitOpsDetector) ExecuteArgoSync(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	argoApp, err := g.detectArgoCDApp(ctx, resource)
	if err != nil || argoApp == nil {
		return fmt.Errorf("no ArgoCD application found for %s/%s", resource.Namespace, resource.Name)
	}

	// Get the Application object
	app := &unstructured.Unstructured{}
	app.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "argoproj.io", Version: "v1alpha1", Kind: "Application",
	})

	if err := g.client.Get(ctx, types.NamespacedName{
		Name: argoApp.Name, Namespace: argoApp.Namespace,
	}, app); err != nil {
		return fmt.Errorf("getting ArgoCD application: %w", err)
	}

	// Set the operation field to trigger a sync
	operation := map[string]interface{}{
		"initiatedBy": map[string]interface{}{
			"automated": false,
			"username":  "chatcli-aiops",
		},
		"sync": map[string]interface{}{
			"prune": false,
		},
	}

	// If a specific revision is requested
	if rev, ok := params["revision"]; ok {
		operation["sync"].(map[string]interface{})["revision"] = rev
	}

	if err := unstructured.SetNestedField(app.Object, operation, "operation"); err != nil {
		return fmt.Errorf("setting sync operation: %w", err)
	}

	return g.client.Update(ctx, app)
}

// buildGitOpsSummary creates a summary of GitOps state.
func (g *GitOpsDetector) buildGitOpsSummary(ctx *GitOpsContext) string {
	var parts []string

	if ctx.HelmRelease != nil {
		r := ctx.HelmRelease
		parts = append(parts, fmt.Sprintf("Helm release '%s' chart=%s version=%s status=%s revision=%d",
			r.Name, r.Chart, r.Version, r.Status, r.Revision))
		if r.Status == "failed" || r.Status == "pending-upgrade" {
			parts = append(parts, fmt.Sprintf("WARNING: Helm release in %s state (previous revision=%d status=%s)",
				r.Status, r.PreviousRevision, r.PreviousStatus))
		}
	}

	if ctx.ArgoCDApp != nil {
		a := ctx.ArgoCDApp
		parts = append(parts, fmt.Sprintf("ArgoCD app '%s' sync=%s health=%s repo=%s policy=%s",
			a.Name, a.SyncStatus, a.HealthStatus, a.RepoURL, a.SyncPolicy))
		if a.HealthStatus == "Degraded" || a.SyncStatus == "OutOfSync" {
			parts = append(parts, fmt.Sprintf("WARNING: ArgoCD app is %s/%s", a.SyncStatus, a.HealthStatus))
		}
		for _, c := range a.Conditions {
			parts = append(parts, fmt.Sprintf("ArgoCD condition: %s", c))
		}
	}

	if ctx.FluxResource != nil {
		f := ctx.FluxResource
		parts = append(parts, fmt.Sprintf("Flux kustomization '%s' ready=%t source=%s",
			f.Name, f.Ready, f.SourceRef))
		if !f.Ready {
			parts = append(parts, "WARNING: Flux kustomization is not ready")
			for _, c := range f.Conditions {
				parts = append(parts, fmt.Sprintf("Flux condition: %s", c))
			}
		}
	}

	if len(parts) == 0 {
		return "No GitOps tooling (Helm/ArgoCD/Flux) detected for this resource."
	}

	return strings.Join(parts, "\n")
}

// FormatForAI formats the GitOps context for LLM consumption.
func (ctx *GitOpsContext) FormatForAI() string {
	if ctx == nil {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## GitOps / Deployment Pipeline Context\n\n")

	if ctx.HelmRelease != nil {
		r := ctx.HelmRelease
		sb.WriteString("### Helm Release\n")
		sb.WriteString(fmt.Sprintf("- Release: %s (namespace: %s)\n", r.Name, r.Namespace))
		sb.WriteString(fmt.Sprintf("- Chart: %s version=%s appVersion=%s\n", r.Chart, r.Version, r.AppVersion))
		sb.WriteString(fmt.Sprintf("- Status: %s (revision %d)\n", r.Status, r.Revision))
		if !r.UpdatedAt.IsZero() {
			sb.WriteString(fmt.Sprintf("- Last deployed: %s\n", r.UpdatedAt.Format(time.RFC3339)))
		}
		if r.PreviousRevision > 0 {
			sb.WriteString(fmt.Sprintf("- Previous revision: %d (status=%s) — available for rollback\n",
				r.PreviousRevision, r.PreviousStatus))
		}
		if r.Status == "failed" || r.Status == "pending-upgrade" {
			sb.WriteString(fmt.Sprintf("**ALERT: Helm release is in '%s' state — this is likely the cause of the issue.**\n", r.Status))
		}
		sb.WriteString("\n")
	}

	if ctx.ArgoCDApp != nil {
		a := ctx.ArgoCDApp
		sb.WriteString("### ArgoCD Application\n")
		sb.WriteString(fmt.Sprintf("- Application: %s (project: %s)\n", a.Name, a.Project))
		sb.WriteString(fmt.Sprintf("- Repository: %s path=%s revision=%s\n", a.RepoURL, a.Path, a.TargetRevision))
		sb.WriteString(fmt.Sprintf("- Sync: %s | Health: %s | Policy: %s\n", a.SyncStatus, a.HealthStatus, a.SyncPolicy))
		if !a.LastSyncedAt.IsZero() {
			sb.WriteString(fmt.Sprintf("- Last sync: %s result=%s\n", a.LastSyncedAt.Format(time.RFC3339), a.LastSyncResult))
		}
		if a.HealthStatus == "Degraded" {
			sb.WriteString("**ALERT: ArgoCD app health is Degraded.**\n")
		}
		if a.SyncStatus == "OutOfSync" {
			sb.WriteString("**ALERT: ArgoCD app is OutOfSync — cluster state differs from git.**\n")
		}
		for _, c := range a.Conditions {
			sb.WriteString(fmt.Sprintf("- Condition: %s\n", c))
		}
		sb.WriteString("\n")
	}

	if ctx.FluxResource != nil {
		f := ctx.FluxResource
		sb.WriteString("### Flux Kustomization\n")
		sb.WriteString(fmt.Sprintf("- Kustomization: %s (namespace: %s)\n", f.Name, f.Namespace))
		sb.WriteString(fmt.Sprintf("- Source: %s path=%s\n", f.SourceRef, f.Path))
		sb.WriteString(fmt.Sprintf("- Ready: %t suspended=%t\n", f.Ready, f.Suspended))
		if !f.LastApplied.IsZero() {
			sb.WriteString(fmt.Sprintf("- Last applied: %s\n", f.LastApplied.Format(time.RFC3339)))
		}
		if !f.Ready {
			sb.WriteString("**ALERT: Flux kustomization is not ready.**\n")
			for _, c := range f.Conditions {
				sb.WriteString(fmt.Sprintf("- %s\n", c))
			}
		}
		sb.WriteString("\n")
	}

	result := sb.String()
	if len(result) > 3000 {
		result = result[:2997] + "..."
	}
	return result
}
