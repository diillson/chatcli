package controllers

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apiresource "k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// BlastRadiusPredictor predicts the impact of remediation actions before execution.
// It validates safety constraints, checks PDB violations, resource quotas,
// node capacity, and dependent service disruption.
type BlastRadiusPredictor struct {
	client client.Client
}

// BlastRadiusPrediction contains the predicted impact of an action.
type BlastRadiusPrediction struct {
	// Safe indicates whether the action is considered safe to execute.
	Safe bool
	// RiskLevel is "low", "medium", "high", or "critical".
	RiskLevel string
	// Warnings are potential issues that don't block execution.
	Warnings []string
	// Blockers are issues that should prevent execution.
	Blockers []string
	// AffectedServices lists services that would be impacted.
	AffectedServices []AffectedService
	// PDBCheck describes PodDisruptionBudget impact.
	PDBCheck *PDBCheckResult
	// QuotaCheck describes ResourceQuota impact.
	QuotaCheck *QuotaCheckResult
	// NodeCapacityCheck describes node capacity impact.
	NodeCapacityCheck *NodeCapacityResult
	// Summary is a human-readable prediction summary.
	Summary string
}

// AffectedService describes a service impacted by the remediation.
type AffectedService struct {
	Name            string
	Namespace       string
	CurrentHealth   string
	PredictedHealth string
	Reason          string
}

// PDBCheckResult describes PodDisruptionBudget impact.
type PDBCheckResult struct {
	PDBName            string
	MinAvailable       string
	MaxUnavailable     string
	CurrentHealthy     int32
	AllowedDisruptions int32
	WouldViolate       bool
	Detail             string
}

// QuotaCheckResult describes ResourceQuota impact.
type QuotaCheckResult struct {
	QuotaName    string
	WouldExceed  bool
	CPURemaining string
	MemRemaining string
	Detail       string
}

// NodeCapacityResult describes node capacity impact.
type NodeCapacityResult struct {
	TargetNode     string
	CPUAllocatable string
	MemAllocatable string
	CPURequested   string
	MemRequested   string
	WouldExceed    bool
	Detail         string
}

// NewBlastRadiusPredictor creates a new BlastRadiusPredictor.
func NewBlastRadiusPredictor(c client.Client) *BlastRadiusPredictor {
	return &BlastRadiusPredictor{client: c}
}

// PredictImpact predicts the impact of a remediation action.
func (bp *BlastRadiusPredictor) PredictImpact(ctx context.Context, resource platformv1alpha1.ResourceRef, action *platformv1alpha1.RemediationAction) (*BlastRadiusPrediction, error) {
	prediction := &BlastRadiusPrediction{
		Safe:      true,
		RiskLevel: "low",
	}

	switch action.Type {
	case platformv1alpha1.ActionScaleDeployment:
		bp.predictScaleImpact(ctx, resource, action.Params, prediction)
	case platformv1alpha1.ActionRollbackDeployment, platformv1alpha1.ActionHelmRollback:
		bp.predictRollbackImpact(ctx, resource, prediction)
	case platformv1alpha1.ActionRestartDeployment:
		bp.predictRestartImpact(ctx, resource, prediction)
	case platformv1alpha1.ActionDeletePod:
		bp.predictDeletePodImpact(ctx, resource, action.Params, prediction)
	case platformv1alpha1.ActionAdjustResources:
		bp.predictResourceAdjustImpact(ctx, resource, action.Params, prediction)
	case platformv1alpha1.ActionCordonNode, platformv1alpha1.ActionDrainNode:
		bp.predictNodeImpact(ctx, resource, action, prediction)
	case platformv1alpha1.ActionAdjustHPA:
		bp.predictHPAAdjustImpact(ctx, resource, action.Params, prediction)

	// StatefulSet
	case platformv1alpha1.ActionScaleStatefulSet:
		bp.predictScaleStatefulSetImpact(ctx, resource, action.Params, prediction)
	case platformv1alpha1.ActionRestartStatefulSet:
		bp.predictRestartWorkloadImpact(ctx, resource, prediction, "StatefulSet")
	case platformv1alpha1.ActionRollbackStatefulSet, platformv1alpha1.ActionRollbackDaemonSet:
		prediction.Warnings = append(prediction.Warnings,
			"ControllerRevision rollback will trigger rolling update")
	case platformv1alpha1.ActionAdjustStatefulSetResources, platformv1alpha1.ActionAdjustDaemonSetResources,
		platformv1alpha1.ActionAdjustJobResources, platformv1alpha1.ActionAdjustCronJobResources:
		bp.predictResourceAdjustImpact(ctx, resource, action.Params, prediction)
	case platformv1alpha1.ActionDeleteStatefulSetPod, platformv1alpha1.ActionDeleteDaemonSetPod:
		bp.predictDeleteWorkloadPodImpact(ctx, resource, prediction)
	case platformv1alpha1.ActionForceDeleteStatefulSetPod:
		prediction.Warnings = append(prediction.Warnings,
			"Force-deleting a StatefulSet pod (grace=0) — risk of data corruption if pod has unflushed writes")
	case platformv1alpha1.ActionRecreateStatefulSetPVC:
		prediction.Blockers = append(prediction.Blockers,
			"RecreateStatefulSetPVC will DELETE data — ensure backups exist")
		prediction.Safe = false
	case platformv1alpha1.ActionPartitionStatefulSetUpdate:
		prediction.Warnings = append(prediction.Warnings,
			"Setting partition for canary rollout — only pods with ordinal >= partition will be updated")

	// DaemonSet
	case platformv1alpha1.ActionRestartDaemonSet:
		bp.predictDaemonSetClusterImpact(ctx, resource, prediction, "restart")
	case platformv1alpha1.ActionPauseDaemonSetRollout:
		// Low risk — just pausing
	case platformv1alpha1.ActionCordonAndDeleteDaemonSetPod:
		prediction.Warnings = append(prediction.Warnings,
			"CordonAndDelete: will cordon node AND delete DaemonSet pod — medium risk compound action")

	// Job
	case platformv1alpha1.ActionRetryJob:
		prediction.Warnings = append(prediction.Warnings, "RetryJob: will delete failed Job and create a new one")
	case platformv1alpha1.ActionForceDeleteJobPods:
		prediction.Warnings = append(prediction.Warnings,
			"Force-deleting all Job pods (grace=0) — running work will be interrupted")
	case platformv1alpha1.ActionSuspendJob, platformv1alpha1.ActionResumeJob,
		platformv1alpha1.ActionSuspendCronJob, platformv1alpha1.ActionResumeCronJob:
		// Low risk — suspend/resume

	// CronJob
	case platformv1alpha1.ActionTriggerCronJob:
		bp.predictTriggerCronJobImpact(ctx, resource, prediction)
	case platformv1alpha1.ActionDeleteCronJobActiveJobs:
		prediction.Warnings = append(prediction.Warnings,
			"Deleting active CronJob jobs — running workloads will be interrupted")
	case platformv1alpha1.ActionReplaceCronJobTemplate:
		prediction.Warnings = append(prediction.Warnings,
			"Replacing CronJob template — all future jobs will use the new template")
	}

	// Check PDB
	bp.checkPDB(ctx, resource, prediction)

	// Check resource quotas
	bp.checkQuota(ctx, resource, prediction)

	// Find affected downstream services
	bp.findAffectedServices(ctx, resource, prediction)

	// Determine overall risk level
	bp.computeRiskLevel(prediction)

	// Build summary
	prediction.Summary = bp.buildPredictionSummary(prediction)

	return prediction, nil
}

func (bp *BlastRadiusPredictor) predictScaleImpact(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string, prediction *BlastRadiusPrediction) {
	replicasStr, ok := params["replicas"]
	if !ok {
		return
	}
	var target int32
	fmt.Sscanf(replicasStr, "%d", &target)

	var deploy appsv1.Deployment
	if err := bp.client.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &deploy); err != nil {
		return
	}

	current := int32(1)
	if deploy.Spec.Replicas != nil {
		current = *deploy.Spec.Replicas
	}

	if target < current {
		prediction.Warnings = append(prediction.Warnings,
			fmt.Sprintf("Scaling down from %d to %d replicas — reduced capacity", current, target))
		if target == 1 {
			prediction.Warnings = append(prediction.Warnings,
				"WARNING: Scaling to 1 replica removes all redundancy")
		}
	}

	if target == 0 {
		prediction.Blockers = append(prediction.Blockers,
			"BLOCKED: Scaling to 0 replicas would cause complete service outage")
		prediction.Safe = false
	}

	if target > current*3 {
		prediction.Warnings = append(prediction.Warnings,
			fmt.Sprintf("Large scale-up from %d to %d replicas — check node capacity", current, target))
	}
}

func (bp *BlastRadiusPredictor) predictRollbackImpact(ctx context.Context, resource platformv1alpha1.ResourceRef, prediction *BlastRadiusPrediction) {
	prediction.Warnings = append(prediction.Warnings,
		"Rollback will trigger rolling update — brief period of mixed versions")

	var deploy appsv1.Deployment
	if err := bp.client.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &deploy); err != nil {
		return
	}

	// Check if there's enough capacity for surge
	if deploy.Spec.Strategy.RollingUpdate != nil && deploy.Spec.Strategy.RollingUpdate.MaxSurge != nil {
		prediction.Warnings = append(prediction.Warnings,
			fmt.Sprintf("Rolling update maxSurge=%s — temporary additional pods during rollback",
				deploy.Spec.Strategy.RollingUpdate.MaxSurge.String()))
	}
}

func (bp *BlastRadiusPredictor) predictRestartImpact(ctx context.Context, resource platformv1alpha1.ResourceRef, prediction *BlastRadiusPrediction) {
	var deploy appsv1.Deployment
	if err := bp.client.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &deploy); err != nil {
		return
	}

	replicas := int32(1)
	if deploy.Spec.Replicas != nil {
		replicas = *deploy.Spec.Replicas
	}

	if replicas == 1 {
		prediction.Warnings = append(prediction.Warnings,
			"WARNING: Restarting single-replica deployment — brief downtime expected")
	}
}

func (bp *BlastRadiusPredictor) predictDeletePodImpact(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string, prediction *BlastRadiusPrediction) {
	bp.predictDeleteWorkloadPodImpact(ctx, resource, prediction)
}

func (bp *BlastRadiusPredictor) predictDeleteWorkloadPodImpact(ctx context.Context, resource platformv1alpha1.ResourceRef, prediction *BlastRadiusPrediction) {
	var podList corev1.PodList
	if err := bp.client.List(ctx, &podList, client.InNamespace(resource.Namespace)); err != nil {
		return
	}

	var matchingPods int
	for i := range podList.Items {
		if isResourcePod(&podList.Items[i], resource) {
			matchingPods++
		}
	}

	if matchingPods <= 1 {
		prediction.Blockers = append(prediction.Blockers,
			"BLOCKED: Only 1 pod exists — deleting it would cause full outage")
		prediction.Safe = false
	} else if matchingPods == 2 {
		prediction.Warnings = append(prediction.Warnings,
			"Deleting 1 of 2 pods — 50% capacity reduction")
	}
}

func (bp *BlastRadiusPredictor) predictScaleStatefulSetImpact(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string, prediction *BlastRadiusPrediction) {
	replicasStr, ok := params["replicas"]
	if !ok {
		return
	}
	var target int32
	fmt.Sscanf(replicasStr, "%d", &target)

	var sts appsv1.StatefulSet
	if err := bp.client.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &sts); err != nil {
		return
	}

	current := int32(1)
	if sts.Spec.Replicas != nil {
		current = *sts.Spec.Replicas
	}

	if target == 0 {
		prediction.Blockers = append(prediction.Blockers,
			"BLOCKED: Scaling StatefulSet to 0 would cause complete outage")
		prediction.Safe = false
	}

	if target < current {
		prediction.Warnings = append(prediction.Warnings,
			fmt.Sprintf("Scaling StatefulSet down from %d to %d — highest ordinal pods removed first (may be primary)", current, target))
	}

	if target > current*3 {
		prediction.Warnings = append(prediction.Warnings,
			fmt.Sprintf("Large StatefulSet scale-up from %d to %d — check PVC provisioning capacity", current, target))
	}
}

func (bp *BlastRadiusPredictor) predictRestartWorkloadImpact(ctx context.Context, resource platformv1alpha1.ResourceRef, prediction *BlastRadiusPrediction, kind string) {
	switch kind {
	case "StatefulSet":
		var sts appsv1.StatefulSet
		if err := bp.client.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &sts); err != nil {
			return
		}
		replicas := int32(1)
		if sts.Spec.Replicas != nil {
			replicas = *sts.Spec.Replicas
		}
		if replicas == 1 {
			prediction.Warnings = append(prediction.Warnings,
				"Restarting single-replica StatefulSet — brief downtime expected")
		}
		prediction.Warnings = append(prediction.Warnings,
			"StatefulSet rolling restart follows ordinal order (reverse)")
	}
}

func (bp *BlastRadiusPredictor) predictDaemonSetClusterImpact(ctx context.Context, resource platformv1alpha1.ResourceRef, prediction *BlastRadiusPrediction, action string) {
	var ds appsv1.DaemonSet
	if err := bp.client.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &ds); err != nil {
		return
	}

	prediction.Warnings = append(prediction.Warnings,
		fmt.Sprintf("DaemonSet %s affects ALL nodes — %d desired pods cluster-wide", action, ds.Status.DesiredNumberScheduled))

	if ds.Status.DesiredNumberScheduled > 50 {
		prediction.Warnings = append(prediction.Warnings,
			"Large DaemonSet (>50 nodes) — consider using partition/maxUnavailable to limit blast radius")
	}
}

func (bp *BlastRadiusPredictor) predictTriggerCronJobImpact(ctx context.Context, resource platformv1alpha1.ResourceRef, prediction *BlastRadiusPrediction) {
	var cj batchv1.CronJob
	if err := bp.client.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &cj); err != nil {
		return
	}

	if cj.Spec.ConcurrencyPolicy == batchv1.ForbidConcurrent && len(cj.Status.Active) > 0 {
		prediction.Warnings = append(prediction.Warnings,
			"CronJob has concurrencyPolicy=Forbid and active jobs — manual trigger may not run")
	}
}

func (bp *BlastRadiusPredictor) predictResourceAdjustImpact(ctx context.Context, res platformv1alpha1.ResourceRef, params map[string]string, prediction *BlastRadiusPrediction) {
	prediction.Warnings = append(prediction.Warnings,
		"Resource adjustment will trigger rolling restart of all pods")

	// Check if increasing memory significantly
	if memLimit, ok := params["memory_limit"]; ok {
		qty, err := apiresource.ParseQuantity(memLimit)
		if err == nil && qty.Value() > 4*1024*1024*1024 { // > 4Gi
			prediction.Warnings = append(prediction.Warnings,
				fmt.Sprintf("Large memory limit (%s) — ensure nodes have sufficient capacity", memLimit))
		}
	}
}

func (bp *BlastRadiusPredictor) predictNodeImpact(ctx context.Context, res platformv1alpha1.ResourceRef, action *platformv1alpha1.RemediationAction, prediction *BlastRadiusPrediction) {
	nodeName, ok := action.Params["node"]
	if !ok {
		prediction.Blockers = append(prediction.Blockers, "Node name not specified")
		prediction.Safe = false
		return
	}

	// Count pods on the node
	var pods corev1.PodList
	if err := bp.client.List(ctx, &pods); err != nil {
		return
	}

	podsOnNode := 0
	for _, pod := range pods.Items {
		if pod.Spec.NodeName == nodeName {
			podsOnNode++
		}
	}

	if action.Type == platformv1alpha1.ActionDrainNode {
		prediction.Warnings = append(prediction.Warnings,
			fmt.Sprintf("Draining node %s will evict %d pods — ensure other nodes have capacity", nodeName, podsOnNode))
	} else {
		prediction.Warnings = append(prediction.Warnings,
			fmt.Sprintf("Cordoning node %s will prevent new pods from scheduling (currently %d pods)", nodeName, podsOnNode))
	}
}

func (bp *BlastRadiusPredictor) predictHPAAdjustImpact(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string, prediction *BlastRadiusPrediction) {
	if maxStr, ok := params["maxReplicas"]; ok {
		var max int32
		fmt.Sscanf(maxStr, "%d", &max)
		prediction.Warnings = append(prediction.Warnings,
			fmt.Sprintf("Adjusting HPA maxReplicas to %d — ensure cluster has capacity", max))
	}
}

// getPodTemplateLabels returns the pod template labels for any supported workload kind.
func (bp *BlastRadiusPredictor) getPodTemplateLabels(ctx context.Context, res platformv1alpha1.ResourceRef) map[string]string {
	switch res.Kind {
	case "Deployment":
		var d appsv1.Deployment
		if err := bp.client.Get(ctx, types.NamespacedName{Name: res.Name, Namespace: res.Namespace}, &d); err != nil {
			return nil
		}
		return d.Spec.Template.Labels
	case "StatefulSet":
		var s appsv1.StatefulSet
		if err := bp.client.Get(ctx, types.NamespacedName{Name: res.Name, Namespace: res.Namespace}, &s); err != nil {
			return nil
		}
		return s.Spec.Template.Labels
	case "DaemonSet":
		var d appsv1.DaemonSet
		if err := bp.client.Get(ctx, types.NamespacedName{Name: res.Name, Namespace: res.Namespace}, &d); err != nil {
			return nil
		}
		return d.Spec.Template.Labels
	case "Job":
		var j batchv1.Job
		if err := bp.client.Get(ctx, types.NamespacedName{Name: res.Name, Namespace: res.Namespace}, &j); err != nil {
			return nil
		}
		return j.Spec.Template.Labels
	case "CronJob":
		var c batchv1.CronJob
		if err := bp.client.Get(ctx, types.NamespacedName{Name: res.Name, Namespace: res.Namespace}, &c); err != nil {
			return nil
		}
		return c.Spec.JobTemplate.Spec.Template.Labels
	default:
		return nil
	}
}

// checkPDB checks if the action would violate PodDisruptionBudgets.
func (bp *BlastRadiusPredictor) checkPDB(ctx context.Context, res platformv1alpha1.ResourceRef, prediction *BlastRadiusPrediction) {
	var pdbList policyv1.PodDisruptionBudgetList
	if err := bp.client.List(ctx, &pdbList, client.InNamespace(res.Namespace)); err != nil {
		return
	}

	podLabels := bp.getPodTemplateLabels(ctx, res)
	if podLabels == nil {
		return
	}

	for _, pdb := range pdbList.Items {
		if pdb.Spec.Selector == nil {
			continue
		}

		matches := true
		for k, v := range pdb.Spec.Selector.MatchLabels {
			if podLabels[k] != v {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}

		result := &PDBCheckResult{
			PDBName:            pdb.Name,
			CurrentHealthy:     pdb.Status.CurrentHealthy,
			AllowedDisruptions: pdb.Status.DisruptionsAllowed,
		}

		if pdb.Spec.MinAvailable != nil {
			result.MinAvailable = pdb.Spec.MinAvailable.String()
		}
		if pdb.Spec.MaxUnavailable != nil {
			result.MaxUnavailable = pdb.Spec.MaxUnavailable.String()
		}

		if pdb.Status.DisruptionsAllowed == 0 {
			result.WouldViolate = true
			result.Detail = fmt.Sprintf("PDB %s allows 0 disruptions (healthy=%d)", pdb.Name, pdb.Status.CurrentHealthy)
			prediction.Blockers = append(prediction.Blockers,
				fmt.Sprintf("PDB violation: %s allows 0 disruptions", pdb.Name))
			prediction.Safe = false
		} else {
			result.Detail = fmt.Sprintf("PDB %s allows %d disruptions (healthy=%d)",
				pdb.Name, pdb.Status.DisruptionsAllowed, pdb.Status.CurrentHealthy)
		}

		prediction.PDBCheck = result
		break
	}
}

// checkQuota checks if the action would exceed ResourceQuotas.
func (bp *BlastRadiusPredictor) checkQuota(ctx context.Context, res platformv1alpha1.ResourceRef, prediction *BlastRadiusPrediction) {
	var quotas corev1.ResourceQuotaList
	if err := bp.client.List(ctx, &quotas, client.InNamespace(res.Namespace)); err != nil {
		return
	}

	for _, q := range quotas.Items {
		result := &QuotaCheckResult{
			QuotaName: q.Name,
		}

		cpuUsed := q.Status.Used[corev1.ResourceRequestsCPU]
		cpuHard := q.Status.Hard[corev1.ResourceRequestsCPU]
		memUsed := q.Status.Used[corev1.ResourceRequestsMemory]
		memHard := q.Status.Hard[corev1.ResourceRequestsMemory]

		if !cpuHard.IsZero() {
			remaining := cpuHard.DeepCopy()
			remaining.Sub(cpuUsed)
			result.CPURemaining = remaining.String()
		}
		if !memHard.IsZero() {
			remaining := memHard.DeepCopy()
			remaining.Sub(memUsed)
			result.MemRemaining = remaining.String()
		}

		// Check if quota is near limit (>90% used)
		if !cpuHard.IsZero() && cpuUsed.AsApproximateFloat64() > cpuHard.AsApproximateFloat64()*0.9 {
			prediction.Warnings = append(prediction.Warnings,
				fmt.Sprintf("CPU quota %s is >90%% used (used=%s hard=%s)",
					q.Name, cpuUsed.String(), cpuHard.String()))
		}
		if !memHard.IsZero() && memUsed.AsApproximateFloat64() > memHard.AsApproximateFloat64()*0.9 {
			prediction.Warnings = append(prediction.Warnings,
				fmt.Sprintf("Memory quota %s is >90%% used (used=%s hard=%s)",
					q.Name, memUsed.String(), memHard.String()))
		}

		result.Detail = fmt.Sprintf("Quota %s: CPU remaining=%s, Memory remaining=%s",
			q.Name, result.CPURemaining, result.MemRemaining)

		prediction.QuotaCheck = result
		break
	}
}

// findAffectedServices discovers services that depend on the resource.
func (bp *BlastRadiusPredictor) findAffectedServices(ctx context.Context, res platformv1alpha1.ResourceRef, prediction *BlastRadiusPrediction) {
	var services corev1.ServiceList
	if err := bp.client.List(ctx, &services, client.InNamespace(res.Namespace)); err != nil {
		return
	}

	podLabels := bp.getPodTemplateLabels(ctx, res)
	if podLabels == nil {
		return
	}

	for _, svc := range services.Items {
		if len(svc.Spec.Selector) == 0 {
			continue
		}
		matches := true
		for k, v := range svc.Spec.Selector {
			if podLabels[k] != v {
				matches = false
				break
			}
		}
		if matches {
			prediction.AffectedServices = append(prediction.AffectedServices, AffectedService{
				Name:            svc.Name,
				Namespace:       svc.Namespace,
				CurrentHealth:   "serving",
				PredictedHealth: "may be disrupted during remediation",
				Reason:          fmt.Sprintf("Service %s routes to %s pods", svc.Name, res.Name),
			})
		}
	}
}

// computeRiskLevel determines overall risk based on all checks.
func (bp *BlastRadiusPredictor) computeRiskLevel(prediction *BlastRadiusPrediction) {
	if len(prediction.Blockers) > 0 {
		prediction.RiskLevel = "critical"
		prediction.Safe = false
		return
	}

	warningCount := len(prediction.Warnings)
	affectedCount := len(prediction.AffectedServices)

	switch {
	case warningCount >= 3 || affectedCount >= 3:
		prediction.RiskLevel = "high"
	case warningCount >= 1 || affectedCount >= 1:
		prediction.RiskLevel = "medium"
	default:
		prediction.RiskLevel = "low"
	}
}

func (bp *BlastRadiusPredictor) buildPredictionSummary(prediction *BlastRadiusPrediction) string {
	var parts []string

	parts = append(parts, fmt.Sprintf("Risk: %s, Safe: %t", prediction.RiskLevel, prediction.Safe))

	if len(prediction.Blockers) > 0 {
		parts = append(parts, fmt.Sprintf("Blockers: %s", strings.Join(prediction.Blockers, "; ")))
	}
	if len(prediction.Warnings) > 0 {
		parts = append(parts, fmt.Sprintf("Warnings: %d", len(prediction.Warnings)))
	}
	if len(prediction.AffectedServices) > 0 {
		var names []string
		for _, s := range prediction.AffectedServices {
			names = append(names, s.Name)
		}
		parts = append(parts, fmt.Sprintf("Affected services: %s", strings.Join(names, ", ")))
	}

	return strings.Join(parts, " | ")
}

// FormatForAI formats the blast radius prediction for LLM consumption.
func (p *BlastRadiusPrediction) FormatForAI() string {
	if p == nil {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## Blast Radius / Impact Prediction\n\n")
	sb.WriteString(fmt.Sprintf("**Overall Risk: %s | Safe to Execute: %t**\n\n", p.RiskLevel, p.Safe))

	if len(p.Blockers) > 0 {
		sb.WriteString("### BLOCKERS (action should NOT proceed)\n")
		for _, b := range p.Blockers {
			sb.WriteString(fmt.Sprintf("- %s\n", b))
		}
		sb.WriteString("\n")
	}

	if len(p.Warnings) > 0 {
		sb.WriteString("### Warnings\n")
		for _, w := range p.Warnings {
			sb.WriteString(fmt.Sprintf("- %s\n", w))
		}
		sb.WriteString("\n")
	}

	if p.PDBCheck != nil {
		sb.WriteString("### PodDisruptionBudget\n")
		sb.WriteString(fmt.Sprintf("- %s\n", p.PDBCheck.Detail))
		if p.PDBCheck.WouldViolate {
			sb.WriteString("**PDB would be violated!**\n")
		}
		sb.WriteString("\n")
	}

	if p.QuotaCheck != nil {
		sb.WriteString("### ResourceQuota\n")
		sb.WriteString(fmt.Sprintf("- %s\n", p.QuotaCheck.Detail))
		sb.WriteString("\n")
	}

	if len(p.AffectedServices) > 0 {
		sb.WriteString("### Affected Services\n")
		for _, s := range p.AffectedServices {
			sb.WriteString(fmt.Sprintf("- %s/%s: %s (%s)\n",
				s.Namespace, s.Name, s.PredictedHealth, s.Reason))
		}
		sb.WriteString("\n")
	}

	result := sb.String()
	if len(result) > 2000 {
		result = result[:1997] + "..."
	}
	return result
}

// ensure imports are used
var _ = apiresource.MustParse
var _ = autoscalingv2.MetricSourceType("")
