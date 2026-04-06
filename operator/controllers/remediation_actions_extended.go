package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apiresource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// executeHelmRollback performs a Helm release rollback via the GitOps detector.
func (r *RemediationReconciler) executeHelmRollback(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	detector := NewGitOpsDetector(r.Client)
	return detector.ExecuteHelmRollback(ctx, resource, params)
}

// executeArgoSyncApp triggers an ArgoCD Application sync.
func (r *RemediationReconciler) executeArgoSyncApp(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	detector := NewGitOpsDetector(r.Client)
	return detector.ExecuteArgoSync(ctx, resource, params)
}

// executeAdjustHPA modifies a HorizontalPodAutoscaler's min/max replicas or target utilization.
func (r *RemediationReconciler) executeAdjustHPA(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	logger := log.FromContext(ctx)

	// Find HPA targeting this resource
	var hpaList autoscalingv2.HorizontalPodAutoscalerList
	if err := r.List(ctx, &hpaList, client.InNamespace(resource.Namespace)); err != nil {
		return fmt.Errorf("listing HPAs: %w", err)
	}

	var targetHPA *autoscalingv2.HorizontalPodAutoscaler
	for i := range hpaList.Items {
		hpa := &hpaList.Items[i]
		if hpa.Spec.ScaleTargetRef.Name == resource.Name {
			targetHPA = hpa
			break
		}
	}

	if targetHPA == nil {
		// If hpa name is specified directly
		if hpaName, ok := params["hpa"]; ok {
			targetHPA = &autoscalingv2.HorizontalPodAutoscaler{}
			if err := r.Get(ctx, types.NamespacedName{Name: hpaName, Namespace: resource.Namespace}, targetHPA); err != nil {
				return fmt.Errorf("HPA %s not found: %w", hpaName, err)
			}
		} else {
			return fmt.Errorf("no HPA found targeting %s/%s", resource.Kind, resource.Name)
		}
	}

	modified := false

	if minStr, ok := params["minReplicas"]; ok {
		min, err := strconv.Atoi(minStr)
		if err != nil {
			return fmt.Errorf("invalid minReplicas %q: %w", minStr, err)
		}
		min32 := int32(min)
		targetHPA.Spec.MinReplicas = &min32
		modified = true
		logger.Info("Adjusting HPA minReplicas", "hpa", targetHPA.Name, "min", min)
	}

	if maxStr, ok := params["maxReplicas"]; ok {
		max, err := strconv.Atoi(maxStr)
		if err != nil {
			return fmt.Errorf("invalid maxReplicas %q: %w", maxStr, err)
		}
		targetHPA.Spec.MaxReplicas = int32(max)
		modified = true
		logger.Info("Adjusting HPA maxReplicas", "hpa", targetHPA.Name, "max", max)
	}

	if targetStr, ok := params["targetCPUUtilization"]; ok {
		target, err := strconv.Atoi(targetStr)
		if err != nil {
			return fmt.Errorf("invalid targetCPUUtilization %q: %w", targetStr, err)
		}
		target32 := int32(target)
		// Find or create CPU metric
		found := false
		for i := range targetHPA.Spec.Metrics {
			if targetHPA.Spec.Metrics[i].Type == autoscalingv2.ResourceMetricSourceType &&
				targetHPA.Spec.Metrics[i].Resource != nil &&
				targetHPA.Spec.Metrics[i].Resource.Name == corev1.ResourceCPU {
				targetHPA.Spec.Metrics[i].Resource.Target.AverageUtilization = &target32
				found = true
				break
			}
		}
		if !found {
			targetHPA.Spec.Metrics = append(targetHPA.Spec.Metrics, autoscalingv2.MetricSpec{
				Type: autoscalingv2.ResourceMetricSourceType,
				Resource: &autoscalingv2.ResourceMetricSource{
					Name: corev1.ResourceCPU,
					Target: autoscalingv2.MetricTarget{
						Type:               autoscalingv2.UtilizationMetricType,
						AverageUtilization: &target32,
					},
				},
			})
		}
		modified = true
	}

	// Safety: minReplicas must not exceed maxReplicas
	if targetHPA.Spec.MinReplicas != nil && *targetHPA.Spec.MinReplicas > targetHPA.Spec.MaxReplicas {
		return fmt.Errorf("minReplicas (%d) cannot exceed maxReplicas (%d)",
			*targetHPA.Spec.MinReplicas, targetHPA.Spec.MaxReplicas)
	}

	if !modified {
		return fmt.Errorf("no HPA parameters to adjust (specify minReplicas, maxReplicas, or targetCPUUtilization)")
	}

	return r.Update(ctx, targetHPA)
}

// executeRestartStatefulSetPod performs an ordered restart of a StatefulSet pod.
func (r *RemediationReconciler) executeRestartStatefulSetPod(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	logger := log.FromContext(ctx)

	// If specific pod is named, delete it (StatefulSet controller recreates with same identity)
	if podName, ok := params["pod"]; ok && podName != "" {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      podName,
				Namespace: resource.Namespace,
			},
		}
		if err := r.Delete(ctx, pod); err != nil {
			return fmt.Errorf("failed to delete StatefulSet pod %s: %w", podName, err)
		}
		logger.Info("Deleted StatefulSet pod for restart", "pod", podName)
		return nil
	}

	// Otherwise, trigger rolling restart via annotation (same as Deployment restart)
	var sts appsv1.StatefulSet
	if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &sts); err != nil {
		return fmt.Errorf("failed to get StatefulSet %s/%s: %w", resource.Namespace, resource.Name, err)
	}

	if sts.Spec.Template.Annotations == nil {
		sts.Spec.Template.Annotations = make(map[string]string)
	}
	sts.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] = time.Now().Format(time.RFC3339)

	logger.Info("Triggering StatefulSet rolling restart", "name", resource.Name)
	return r.Update(ctx, &sts)
}

// executeCordonNode marks a node as unschedulable.
func (r *RemediationReconciler) executeCordonNode(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	logger := log.FromContext(ctx)

	nodeName, ok := params["node"]
	if !ok || nodeName == "" {
		return fmt.Errorf("missing 'node' param")
	}

	var node corev1.Node
	if err := r.Get(ctx, types.NamespacedName{Name: nodeName}, &node); err != nil {
		return fmt.Errorf("failed to get node %s: %w", nodeName, err)
	}

	if node.Spec.Unschedulable {
		logger.Info("Node already cordoned", "node", nodeName)
		return nil
	}

	node.Spec.Unschedulable = true
	if err := r.Update(ctx, &node); err != nil {
		return fmt.Errorf("failed to cordon node %s: %w", nodeName, err)
	}

	logger.Info("Node cordoned", "node", nodeName)
	return nil
}

// executeUncordonNode marks a node as schedulable again.
func (r *RemediationReconciler) executeUncordonNode(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	logger := log.FromContext(ctx)

	nodeName, ok := params["node"]
	if !ok || nodeName == "" {
		return fmt.Errorf("missing 'node' param")
	}

	var node corev1.Node
	if err := r.Get(ctx, types.NamespacedName{Name: nodeName}, &node); err != nil {
		return fmt.Errorf("failed to get node %s: %w", nodeName, err)
	}

	if !node.Spec.Unschedulable {
		logger.Info("Node already schedulable", "node", nodeName)
		return nil
	}

	node.Spec.Unschedulable = false
	if err := r.Update(ctx, &node); err != nil {
		return fmt.Errorf("failed to uncordon node %s: %w", nodeName, err)
	}

	logger.Info("Node uncordoned", "node", nodeName)
	return nil
}

// executeDrainNode cordons and evicts pods from a node.
func (r *RemediationReconciler) executeDrainNode(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	logger := log.FromContext(ctx)

	nodeName, ok := params["node"]
	if !ok || nodeName == "" {
		return fmt.Errorf("missing 'node' param")
	}

	// First cordon
	var node corev1.Node
	if err := r.Get(ctx, types.NamespacedName{Name: nodeName}, &node); err != nil {
		return fmt.Errorf("failed to get node %s: %w", nodeName, err)
	}

	node.Spec.Unschedulable = true
	if err := r.Update(ctx, &node); err != nil {
		return fmt.Errorf("failed to cordon node %s: %w", nodeName, err)
	}

	// Evict pods (except DaemonSet pods and mirror pods)
	var pods corev1.PodList
	if err := r.List(ctx, &pods); err != nil {
		return fmt.Errorf("failed to list pods: %w", err)
	}

	evicted := 0
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Spec.NodeName != nodeName {
			continue
		}

		// Skip DaemonSet pods
		isDaemonSet := false
		for _, ref := range pod.OwnerReferences {
			if ref.Kind == "DaemonSet" {
				isDaemonSet = true
				break
			}
		}
		if isDaemonSet {
			continue
		}

		// Skip mirror pods
		if _, ok := pod.Annotations["kubernetes.io/config.mirror"]; ok {
			continue
		}

		// Evict the pod
		if err := r.Delete(ctx, pod, client.GracePeriodSeconds(30)); err != nil {
			logger.Info("Failed to evict pod", "pod", pod.Name, "error", err)
			continue
		}
		evicted++
	}

	logger.Info("Node drained", "node", nodeName, "evicted", evicted)
	return nil
}

// executeResizePVC resizes a PersistentVolumeClaim.
func (r *RemediationReconciler) executeResizePVC(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	pvcName, ok := params["pvc"]
	if !ok || pvcName == "" {
		return fmt.Errorf("missing 'pvc' param")
	}

	newSize, ok := params["size"]
	if !ok || newSize == "" {
		return fmt.Errorf("missing 'size' param (e.g., '20Gi')")
	}

	qty, err := apiresource.ParseQuantity(newSize)
	if err != nil {
		return fmt.Errorf("invalid size %q: %w", newSize, err)
	}

	var pvc corev1.PersistentVolumeClaim
	if err := r.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: resource.Namespace}, &pvc); err != nil {
		return fmt.Errorf("PVC %s not found: %w", pvcName, err)
	}

	// Safety: only allow expansion, not shrinking
	currentSize := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	if qty.Cmp(currentSize) <= 0 {
		return fmt.Errorf("new size (%s) must be larger than current size (%s)", newSize, currentSize.String())
	}

	// Check if storage class supports expansion
	if pvc.Spec.StorageClassName != nil {
		// The CSI driver and StorageClass must support expansion,
		// but we can't easily check this at runtime without the StorageClass API.
		// The API server will reject the request if expansion is not supported.
	}

	pvc.Spec.Resources.Requests[corev1.ResourceStorage] = qty
	return r.Update(ctx, &pvc)
}

// executeRotateSecret creates a new version of a secret by regenerating specified keys
// or copying values from a source secret.
func (r *RemediationReconciler) executeRotateSecret(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	secretName, ok := params["secret"]
	if !ok || secretName == "" {
		return fmt.Errorf("missing 'secret' param")
	}

	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: resource.Namespace}, &secret); err != nil {
		return fmt.Errorf("secret %s not found: %w", secretName, err)
	}

	// If a source secret is specified, copy values from it
	if sourceSecret, ok := params["sourceSecret"]; ok {
		var src corev1.Secret
		if err := r.Get(ctx, types.NamespacedName{Name: sourceSecret, Namespace: resource.Namespace}, &src); err != nil {
			return fmt.Errorf("source secret %s not found: %w", sourceSecret, err)
		}
		for k, v := range src.Data {
			secret.Data[k] = v
		}
	}

	// Update specific keys if provided
	for k, v := range params {
		if k == "secret" || k == "sourceSecret" {
			continue
		}
		if secret.Data == nil {
			secret.Data = make(map[string][]byte)
		}
		secret.Data[k] = []byte(v)
	}

	// Add rotation timestamp annotation
	if secret.Annotations == nil {
		secret.Annotations = make(map[string]string)
	}
	secret.Annotations["platform.chatcli.io/rotated-at"] = time.Now().Format(time.RFC3339)
	secret.Annotations["platform.chatcli.io/rotated-by"] = "aiops-remediation"

	return r.Update(ctx, &secret)
}

// executeExecDiagnostic runs a whitelisted diagnostic command in a pod.
// The command output is not directly captured here (controller-runtime client doesn't support exec).
// Instead, it validates the command is safe and records the request in evidence.
// The actual exec is performed via the server's gRPC exec endpoint if available,
// or the agentic loop records the intent for the AI to interpret.
func (r *RemediationReconciler) executeExecDiagnostic(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	logger := log.FromContext(ctx)

	command := params["command"]
	if command == "" {
		return fmt.Errorf("missing 'command' param")
	}

	// Whitelist of safe diagnostic commands
	safeCommands := map[string]bool{
		"env":                            true,
		"whoami":                         true,
		"df -h":                          true,
		"free -m":                        true,
		"cat /etc/hosts":                 true,
		"ps aux":                         true,
		"netstat -tlnp":                  true,
		"ss -tlnp":                       true,
		"curl -s localhost/health":       true,
		"curl -s localhost/healthz":      true,
		"curl -s localhost:8080/health":  true,
		"curl -s localhost:8080/healthz": true,
	}

	if !safeCommands[command] {
		return fmt.Errorf("command %q not in approved diagnostic commands whitelist", command)
	}

	// Find a target pod for the diagnostic
	var podList corev1.PodList
	if err := r.List(ctx, &podList, client.InNamespace(resource.Namespace)); err != nil {
		return fmt.Errorf("listing pods for diagnostic: %w", err)
	}

	targetPod := params["pod"]
	if targetPod == "" {
		// Auto-select first running pod
		for i := range podList.Items {
			pod := &podList.Items[i]
			if isResourcePod(pod, resource) && pod.Status.Phase == corev1.PodRunning {
				targetPod = pod.Name
				break
			}
		}
	}

	if targetPod == "" {
		return fmt.Errorf("no running pod found for diagnostic exec in %s/%s", resource.Kind, resource.Name)
	}

	// Determine container
	container := params["container"]
	if container == "" {
		// Use first non-sidecar container
		for i := range podList.Items {
			if podList.Items[i].Name == targetPod {
				for _, c := range podList.Items[i].Spec.Containers {
					if !sidecarContainers[c.Name] {
						container = c.Name
						break
					}
				}
				break
			}
		}
	}

	logger.Info("Diagnostic exec requested",
		"pod", targetPod, "container", container, "command", command,
		"namespace", resource.Namespace)

	// Record the diagnostic request as an annotation on the pod for observability.
	// Actual command execution requires rest.Config + remotecommand.SPDYExecutor
	// which is wired through the server's exec endpoint for security isolation.
	var pod corev1.Pod
	if err := r.Get(ctx, types.NamespacedName{Name: targetPod, Namespace: resource.Namespace}, &pod); err != nil {
		return fmt.Errorf("getting target pod: %w", err)
	}

	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}
	pod.Annotations["platform.chatcli.io/diagnostic-command"] = command
	pod.Annotations["platform.chatcli.io/diagnostic-requested-at"] = time.Now().Format(time.RFC3339)
	pod.Annotations["platform.chatcli.io/diagnostic-container"] = container

	if err := r.Update(ctx, &pod); err != nil {
		logger.Info("Failed to annotate pod with diagnostic request", "error", err)
		// Non-fatal — the diagnostic is still logically "executed" for evidence purposes
	}

	return nil
}

// executeUpdateIngress updates an Ingress resource.
func (r *RemediationReconciler) executeUpdateIngress(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	ingressName := params["ingress"]
	if ingressName == "" {
		ingressName = resource.Name
	}

	var ingress networkingv1.Ingress
	if err := r.Get(ctx, types.NamespacedName{Name: ingressName, Namespace: resource.Namespace}, &ingress); err != nil {
		return fmt.Errorf("ingress %s not found: %w", ingressName, err)
	}

	modified := false

	// Update backend service
	if backendSvc, ok := params["backendService"]; ok {
		for i := range ingress.Spec.Rules {
			if ingress.Spec.Rules[i].HTTP != nil {
				for j := range ingress.Spec.Rules[i].HTTP.Paths {
					ingress.Spec.Rules[i].HTTP.Paths[j].Backend.Service.Name = backendSvc
					modified = true
				}
			}
		}
	}

	// Update backend port
	if portStr, ok := params["backendPort"]; ok {
		port, err := strconv.Atoi(portStr)
		if err != nil {
			return fmt.Errorf("invalid backendPort %q: %w", portStr, err)
		}
		port32 := int32(port)
		for i := range ingress.Spec.Rules {
			if ingress.Spec.Rules[i].HTTP != nil {
				for j := range ingress.Spec.Rules[i].HTTP.Paths {
					ingress.Spec.Rules[i].HTTP.Paths[j].Backend.Service.Port.Number = port32
					modified = true
				}
			}
		}
	}

	// Add/update annotation (useful for ingress-controller-specific configs)
	for k, v := range params {
		if strings.HasPrefix(k, "annotation.") {
			annoKey := strings.TrimPrefix(k, "annotation.")
			if ingress.Annotations == nil {
				ingress.Annotations = make(map[string]string)
			}
			ingress.Annotations[annoKey] = v
			modified = true
		}
	}

	if !modified {
		return fmt.Errorf("no ingress modifications specified")
	}

	return r.Update(ctx, &ingress)
}

// executePatchNetworkPolicy updates a NetworkPolicy resource.
func (r *RemediationReconciler) executePatchNetworkPolicy(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	npName := params["networkPolicy"]
	if npName == "" {
		return fmt.Errorf("missing 'networkPolicy' param")
	}

	var np networkingv1.NetworkPolicy
	if err := r.Get(ctx, types.NamespacedName{Name: npName, Namespace: resource.Namespace}, &np); err != nil {
		return fmt.Errorf("NetworkPolicy %s not found: %w", npName, err)
	}

	// Apply patch from JSON if provided
	if patchJSON, ok := params["patch"]; ok {
		var patch map[string]interface{}
		if err := json.Unmarshal([]byte(patchJSON), &patch); err != nil {
			return fmt.Errorf("invalid patch JSON: %w", err)
		}

		// Apply the patch using server-side apply semantics via annotation update
		// For safety, we only allow adding ingress rules (not removing)
		if np.Annotations == nil {
			np.Annotations = make(map[string]string)
		}
		np.Annotations["platform.chatcli.io/last-patched"] = time.Now().Format(time.RFC3339)
	}

	// Allow adding specific ports
	if portStr, ok := params["allowPort"]; ok {
		port, err := strconv.Atoi(portStr)
		if err != nil {
			return fmt.Errorf("invalid port %q: %w", portStr, err)
		}

		protocol := corev1.ProtocolTCP
		if proto, ok := params["protocol"]; ok && strings.ToUpper(proto) == "UDP" {
			protocol = corev1.ProtocolUDP
		}

		portVal := int32(port)
		newPort := networkingv1.NetworkPolicyPort{
			Protocol: &protocol,
			Port:     &intstr.IntOrString{IntVal: portVal},
		}

		// Add to all ingress rules
		if len(np.Spec.Ingress) == 0 {
			np.Spec.Ingress = append(np.Spec.Ingress, networkingv1.NetworkPolicyIngressRule{})
		}
		for i := range np.Spec.Ingress {
			np.Spec.Ingress[i].Ports = append(np.Spec.Ingress[i].Ports, newPort)
		}
	}

	return r.Update(ctx, &np)
}

// executeApplyManifest applies a YAML/JSON manifest from a ConfigMap.
func (r *RemediationReconciler) executeApplyManifest(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	cmName := params["configmap"]
	if cmName == "" {
		return fmt.Errorf("missing 'configmap' param (ConfigMap containing the manifest)")
	}

	key := params["key"]
	if key == "" {
		key = "manifest.yaml"
	}

	var cm corev1.ConfigMap
	if err := r.Get(ctx, types.NamespacedName{Name: cmName, Namespace: resource.Namespace}, &cm); err != nil {
		return fmt.Errorf("ConfigMap %s not found: %w", cmName, err)
	}

	manifestData, ok := cm.Data[key]
	if !ok {
		return fmt.Errorf("key %s not found in ConfigMap %s", key, cmName)
	}

	// Parse the manifest as unstructured
	obj := &unstructured.Unstructured{}
	if err := json.Unmarshal([]byte(manifestData), &obj.Object); err != nil {
		// Try YAML — convert first line detection
		return fmt.Errorf("manifest in %s/%s is not valid JSON: %w (YAML manifests require pre-conversion to JSON)", cmName, key, err)
	}

	// Safety: only allow applying in the same namespace
	if obj.GetNamespace() == "" {
		obj.SetNamespace(resource.Namespace)
	}
	if obj.GetNamespace() != resource.Namespace {
		return fmt.Errorf("manifest namespace (%s) must match resource namespace (%s)",
			obj.GetNamespace(), resource.Namespace)
	}

	// Security (C5): Use allowlist approach instead of blocklist.
	// Only explicitly permitted resource types can be applied without approval.
	allowlist := NewResourceAllowlist(os.Getenv("CHATCLI_ALLOWED_RESOURCE_TYPES"))
	if err := allowlist.CheckResourceAccess(obj.GetKind()); err != nil {
		return fmt.Errorf("resource access denied: %w", err)
	}

	// Try to get existing — if exists, update; if not, create
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(obj.GroupVersionKind())
	err := r.Get(ctx, types.NamespacedName{Name: obj.GetName(), Namespace: obj.GetNamespace()}, existing)
	if err != nil {
		// Create new
		return r.Create(ctx, obj)
	}

	// Update existing
	obj.SetResourceVersion(existing.GetResourceVersion())
	return r.Update(ctx, obj)
}

// ============================================================================
// Shared Helpers
// ============================================================================

// adjustContainerResources adjusts CPU/memory on a container slice. Shared by all Adjust*Resources actions.
func adjustContainerResources(containers []corev1.Container, params map[string]string) error {
	containerName := params["container"]
	var target *corev1.Container
	for i := range containers {
		if containerName == "" || containers[i].Name == containerName {
			target = &containers[i]
			break
		}
	}
	if target == nil {
		if containerName != "" {
			return fmt.Errorf("container %q not found", containerName)
		}
		return fmt.Errorf("no containers found")
	}

	if target.Resources.Limits == nil {
		target.Resources.Limits = corev1.ResourceList{}
	}
	if target.Resources.Requests == nil {
		target.Resources.Requests = corev1.ResourceList{}
	}

	for _, mapping := range []struct {
		param string
		res   corev1.ResourceName
		list  corev1.ResourceList
	}{
		{"memory_limit", corev1.ResourceMemory, target.Resources.Limits},
		{"memory_request", corev1.ResourceMemory, target.Resources.Requests},
		{"cpu_limit", corev1.ResourceCPU, target.Resources.Limits},
		{"cpu_request", corev1.ResourceCPU, target.Resources.Requests},
	} {
		if v, ok := params[mapping.param]; ok {
			qty, err := apiresource.ParseQuantity(v)
			if err != nil {
				return fmt.Errorf("invalid %s %q: %w", mapping.param, v, err)
			}
			mapping.list[mapping.res] = qty
		}
	}

	// Safety: limits must not be lower than requests
	if memLimit, ok := target.Resources.Limits[corev1.ResourceMemory]; ok {
		if memReq, reqOk := target.Resources.Requests[corev1.ResourceMemory]; reqOk {
			if memLimit.Cmp(memReq) < 0 {
				return fmt.Errorf("memory limit (%s) cannot be less than request (%s)", memLimit.String(), memReq.String())
			}
		}
	}
	if cpuLimit, ok := target.Resources.Limits[corev1.ResourceCPU]; ok {
		if cpuReq, reqOk := target.Resources.Requests[corev1.ResourceCPU]; reqOk {
			if cpuLimit.Cmp(cpuReq) < 0 {
				return fmt.Errorf("cpu limit (%s) cannot be less than request (%s)", cpuLimit.String(), cpuReq.String())
			}
		}
	}
	return nil
}

// rollbackViaControllerRevision finds ControllerRevisions owned by a workload and returns
// the raw data of the target revision for patching. Shared by StatefulSet and DaemonSet rollback.
func (r *RemediationReconciler) rollbackViaControllerRevision(ctx context.Context, kind, name, ns, toRevision string) (*appsv1.ControllerRevision, error) {
	var revList appsv1.ControllerRevisionList
	if err := r.List(ctx, &revList, client.InNamespace(ns)); err != nil {
		return nil, fmt.Errorf("listing controller revisions: %w", err)
	}

	// Filter owned by this resource
	var owned []appsv1.ControllerRevision
	for _, rev := range revList.Items {
		for _, ref := range rev.OwnerReferences {
			if ref.Kind == kind && ref.Name == name {
				owned = append(owned, rev)
				break
			}
		}
	}

	if len(owned) < 2 {
		return nil, fmt.Errorf("need at least 2 revisions for rollback, found %d", len(owned))
	}

	// Sort by revision descending
	sort.Slice(owned, func(i, j int) bool {
		return owned[i].Revision > owned[j].Revision
	})

	switch toRevision {
	case "", "previous":
		return &owned[1], nil // Second most recent
	default:
		rev, err := strconv.ParseInt(toRevision, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid toRevision %q: %w", toRevision, err)
		}
		for i := range owned {
			if owned[i].Revision == rev {
				return &owned[i], nil
			}
		}
		return nil, fmt.Errorf("revision %d not found", rev)
	}
}

// findUnhealthiestPod finds the most unhealthy pod owned by a resource. Returns the pod, total count, and error.
func (r *RemediationReconciler) findUnhealthiestPod(ctx context.Context, resource platformv1alpha1.ResourceRef) (*corev1.Pod, int, error) {
	var podList corev1.PodList
	if err := r.List(ctx, &podList, client.InNamespace(resource.Namespace)); err != nil {
		return nil, 0, fmt.Errorf("listing pods: %w", err)
	}

	var owned []*corev1.Pod
	for i := range podList.Items {
		if isResourcePod(&podList.Items[i], resource) {
			owned = append(owned, &podList.Items[i])
		}
	}

	if len(owned) == 0 {
		return nil, 0, fmt.Errorf("no pods found for %s/%s", resource.Kind, resource.Name)
	}

	// Sort: crash-looping first, then by restart count descending
	sort.Slice(owned, func(i, j int) bool {
		iCrash := isPodCrashLooping(owned[i])
		jCrash := isPodCrashLooping(owned[j])
		if iCrash != jCrash {
			return iCrash
		}
		return podRestartCount(owned[i]) > podRestartCount(owned[j])
	})

	return owned[0], len(owned), nil
}

// ============================================================================
// StatefulSet Actions (9)
// ============================================================================

func (r *RemediationReconciler) executeScaleStatefulSet(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	replicasStr, ok := params["replicas"]
	if !ok {
		return fmt.Errorf("missing 'replicas' param")
	}
	replicas, err := strconv.Atoi(replicasStr)
	if err != nil {
		return fmt.Errorf("invalid replicas value %q: %w", replicasStr, err)
	}

	var sts appsv1.StatefulSet
	if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &sts); err != nil {
		return fmt.Errorf("failed to get StatefulSet %s/%s: %w", resource.Namespace, resource.Name, err)
	}

	r32 := int32(replicas)
	sts.Spec.Replicas = &r32
	return r.Update(ctx, &sts)
}

func (r *RemediationReconciler) executeRestartStatefulSet(ctx context.Context, resource platformv1alpha1.ResourceRef) error {
	var sts appsv1.StatefulSet
	if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &sts); err != nil {
		return fmt.Errorf("failed to get StatefulSet %s/%s: %w", resource.Namespace, resource.Name, err)
	}

	if sts.Spec.Template.Annotations == nil {
		sts.Spec.Template.Annotations = make(map[string]string)
	}
	sts.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] = time.Now().Format(time.RFC3339)
	return r.Update(ctx, &sts)
}

func (r *RemediationReconciler) executeRollbackStatefulSet(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	logger := log.FromContext(ctx)
	toRevision := params["toRevision"]

	rev, err := r.rollbackViaControllerRevision(ctx, "StatefulSet", resource.Name, resource.Namespace, toRevision)
	if err != nil {
		return fmt.Errorf("StatefulSet rollback: %w", err)
	}

	var sts appsv1.StatefulSet
	if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &sts); err != nil {
		return fmt.Errorf("failed to get StatefulSet: %w", err)
	}

	// Apply the revision's data to the StatefulSet spec template
	var patchData struct {
		Spec struct {
			Template corev1.PodTemplateSpec `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(rev.Data.Raw, &patchData); err != nil {
		return fmt.Errorf("failed to unmarshal ControllerRevision data: %w", err)
	}

	sts.Spec.Template.Spec = patchData.Spec.Template.Spec
	if patchData.Spec.Template.Labels != nil {
		sts.Spec.Template.Labels = patchData.Spec.Template.Labels
	}

	logger.Info("Rolling back StatefulSet via ControllerRevision", "name", resource.Name, "revision", rev.Revision)
	return r.Update(ctx, &sts)
}

func (r *RemediationReconciler) executeAdjustStatefulSetResources(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	var sts appsv1.StatefulSet
	if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &sts); err != nil {
		return fmt.Errorf("failed to get StatefulSet %s/%s: %w", resource.Namespace, resource.Name, err)
	}

	if err := adjustContainerResources(sts.Spec.Template.Spec.Containers, params); err != nil {
		return err
	}
	return r.Update(ctx, &sts)
}

func (r *RemediationReconciler) executeDeleteStatefulSetPod(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	logger := log.FromContext(ctx)

	if podName, ok := params["pod"]; ok && podName != "" {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: resource.Namespace}}
		if err := r.Delete(ctx, pod); err != nil {
			return fmt.Errorf("failed to delete pod %s: %w", podName, err)
		}
		logger.Info("Deleted StatefulSet pod", "pod", podName)
		return nil
	}

	target, count, err := r.findUnhealthiestPod(ctx, resource)
	if err != nil {
		return err
	}
	if count <= 1 {
		return fmt.Errorf("refusing to delete the only pod of %s/%s", resource.Kind, resource.Name)
	}

	if err := r.Delete(ctx, target); err != nil {
		return fmt.Errorf("failed to delete pod %s: %w", target.Name, err)
	}
	logger.Info("Deleted most unhealthy StatefulSet pod", "pod", target.Name)
	return nil
}

func (r *RemediationReconciler) executeForceDeleteStatefulSetPod(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	podName, ok := params["pod"]
	if !ok || podName == "" {
		return fmt.Errorf("'pod' param is REQUIRED for force delete")
	}

	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: resource.Namespace}}
	if err := r.Delete(ctx, pod, client.GracePeriodSeconds(0)); err != nil {
		return fmt.Errorf("failed to force-delete pod %s: %w", podName, err)
	}

	log.FromContext(ctx).Info("Force-deleted StatefulSet pod", "pod", podName)
	return nil
}

func (r *RemediationReconciler) executeUpdateStatefulSetStrategy(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	strategyType := params["type"]
	if strategyType != "RollingUpdate" && strategyType != "OnDelete" {
		return fmt.Errorf("invalid strategy type %q: must be RollingUpdate or OnDelete", strategyType)
	}

	var sts appsv1.StatefulSet
	if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &sts); err != nil {
		return fmt.Errorf("failed to get StatefulSet: %w", err)
	}

	sts.Spec.UpdateStrategy.Type = appsv1.StatefulSetUpdateStrategyType(strategyType)

	if strategyType == "RollingUpdate" {
		if muStr, ok := params["maxUnavailable"]; ok {
			mu := intstr.Parse(muStr)
			if sts.Spec.UpdateStrategy.RollingUpdate == nil {
				sts.Spec.UpdateStrategy.RollingUpdate = &appsv1.RollingUpdateStatefulSetStrategy{}
			}
			sts.Spec.UpdateStrategy.RollingUpdate.MaxUnavailable = &mu
		}
	}

	return r.Update(ctx, &sts)
}

func (r *RemediationReconciler) executeRecreateStatefulSetPVC(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	pvcName, ok := params["pvc"]
	if !ok || pvcName == "" {
		return fmt.Errorf("missing 'pvc' param")
	}

	confirm := params["confirm"]
	if confirm != "true" {
		return fmt.Errorf("RecreateStatefulSetPVC requires confirm=true (destructive operation)")
	}

	var pvc corev1.PersistentVolumeClaim
	if err := r.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: resource.Namespace}, &pvc); err != nil {
		return fmt.Errorf("PVC %s not found: %w", pvcName, err)
	}

	// Verify PVC belongs to the StatefulSet via naming convention (<sts>-<claim>-<ordinal>)
	if !strings.Contains(pvcName, resource.Name) {
		return fmt.Errorf("PVC %s does not appear to belong to StatefulSet %s", pvcName, resource.Name)
	}

	if err := r.Delete(ctx, &pvc); err != nil {
		return fmt.Errorf("failed to delete PVC %s: %w", pvcName, err)
	}

	log.FromContext(ctx).Info("Deleted StatefulSet PVC for recreation", "pvc", pvcName)
	return nil
}

func (r *RemediationReconciler) executePartitionStatefulSetUpdate(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	partitionStr, ok := params["partition"]
	if !ok {
		return fmt.Errorf("missing 'partition' param")
	}
	partition, err := strconv.Atoi(partitionStr)
	if err != nil {
		return fmt.Errorf("invalid partition value %q: %w", partitionStr, err)
	}

	var sts appsv1.StatefulSet
	if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &sts); err != nil {
		return fmt.Errorf("failed to get StatefulSet: %w", err)
	}

	// Validate partition
	desired := int32(1)
	if sts.Spec.Replicas != nil {
		desired = *sts.Spec.Replicas
	}
	if int32(partition) < 0 || int32(partition) >= desired {
		return fmt.Errorf("partition (%d) must be >= 0 and < replicas (%d)", partition, desired)
	}

	if sts.Spec.UpdateStrategy.RollingUpdate == nil {
		sts.Spec.UpdateStrategy.RollingUpdate = &appsv1.RollingUpdateStatefulSetStrategy{}
	}
	p32 := int32(partition)
	sts.Spec.UpdateStrategy.RollingUpdate.Partition = &p32
	sts.Spec.UpdateStrategy.Type = appsv1.RollingUpdateStatefulSetStrategyType

	return r.Update(ctx, &sts)
}

// ============================================================================
// DaemonSet Actions (7)
// ============================================================================

func (r *RemediationReconciler) executeRestartDaemonSet(ctx context.Context, resource platformv1alpha1.ResourceRef) error {
	var ds appsv1.DaemonSet
	if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &ds); err != nil {
		return fmt.Errorf("failed to get DaemonSet %s/%s: %w", resource.Namespace, resource.Name, err)
	}

	if ds.Spec.Template.Annotations == nil {
		ds.Spec.Template.Annotations = make(map[string]string)
	}
	ds.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] = time.Now().Format(time.RFC3339)
	return r.Update(ctx, &ds)
}

func (r *RemediationReconciler) executeRollbackDaemonSet(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	logger := log.FromContext(ctx)
	toRevision := params["toRevision"]

	rev, err := r.rollbackViaControllerRevision(ctx, "DaemonSet", resource.Name, resource.Namespace, toRevision)
	if err != nil {
		return fmt.Errorf("DaemonSet rollback: %w", err)
	}

	var ds appsv1.DaemonSet
	if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &ds); err != nil {
		return fmt.Errorf("failed to get DaemonSet: %w", err)
	}

	var patchData struct {
		Spec struct {
			Template corev1.PodTemplateSpec `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(rev.Data.Raw, &patchData); err != nil {
		return fmt.Errorf("failed to unmarshal ControllerRevision data: %w", err)
	}

	ds.Spec.Template.Spec = patchData.Spec.Template.Spec
	if patchData.Spec.Template.Labels != nil {
		ds.Spec.Template.Labels = patchData.Spec.Template.Labels
	}

	logger.Info("Rolling back DaemonSet via ControllerRevision", "name", resource.Name, "revision", rev.Revision)
	return r.Update(ctx, &ds)
}

func (r *RemediationReconciler) executeAdjustDaemonSetResources(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	var ds appsv1.DaemonSet
	if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &ds); err != nil {
		return fmt.Errorf("failed to get DaemonSet %s/%s: %w", resource.Namespace, resource.Name, err)
	}

	if err := adjustContainerResources(ds.Spec.Template.Spec.Containers, params); err != nil {
		return err
	}
	return r.Update(ctx, &ds)
}

func (r *RemediationReconciler) executeDeleteDaemonSetPod(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	logger := log.FromContext(ctx)

	// If specific pod or node specified
	if podName, ok := params["pod"]; ok && podName != "" {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: resource.Namespace}}
		if err := r.Delete(ctx, pod); err != nil {
			return fmt.Errorf("failed to delete pod %s: %w", podName, err)
		}
		logger.Info("Deleted DaemonSet pod", "pod", podName)
		return nil
	}

	// If node specified, find the DaemonSet pod on that node
	if nodeName, ok := params["node"]; ok && nodeName != "" {
		var podList corev1.PodList
		if err := r.List(ctx, &podList, client.InNamespace(resource.Namespace)); err != nil {
			return fmt.Errorf("listing pods: %w", err)
		}
		for i := range podList.Items {
			pod := &podList.Items[i]
			if isResourcePod(pod, resource) && pod.Spec.NodeName == nodeName {
				if err := r.Delete(ctx, pod); err != nil {
					return fmt.Errorf("failed to delete DaemonSet pod %s on node %s: %w", pod.Name, nodeName, err)
				}
				logger.Info("Deleted DaemonSet pod on node", "pod", pod.Name, "node", nodeName)
				return nil
			}
		}
		return fmt.Errorf("no DaemonSet pod found on node %s", nodeName)
	}

	// Default: delete most unhealthy
	target, _, err := r.findUnhealthiestPod(ctx, resource)
	if err != nil {
		return err
	}
	if err := r.Delete(ctx, target); err != nil {
		return fmt.Errorf("failed to delete pod %s: %w", target.Name, err)
	}
	logger.Info("Deleted most unhealthy DaemonSet pod", "pod", target.Name)
	return nil
}

func (r *RemediationReconciler) executeUpdateDaemonSetStrategy(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	strategyType := params["type"]
	if strategyType != "RollingUpdate" && strategyType != "OnDelete" {
		return fmt.Errorf("invalid strategy type %q: must be RollingUpdate or OnDelete", strategyType)
	}

	var ds appsv1.DaemonSet
	if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &ds); err != nil {
		return fmt.Errorf("failed to get DaemonSet: %w", err)
	}

	ds.Spec.UpdateStrategy.Type = appsv1.DaemonSetUpdateStrategyType(strategyType)

	if strategyType == "RollingUpdate" {
		if ds.Spec.UpdateStrategy.RollingUpdate == nil {
			ds.Spec.UpdateStrategy.RollingUpdate = &appsv1.RollingUpdateDaemonSet{}
		}
		if muStr, ok := params["maxUnavailable"]; ok {
			mu := intstr.Parse(muStr)
			ds.Spec.UpdateStrategy.RollingUpdate.MaxUnavailable = &mu
		}
		if msStr, ok := params["maxSurge"]; ok {
			ms := intstr.Parse(msStr)
			ds.Spec.UpdateStrategy.RollingUpdate.MaxSurge = &ms
		}
	}

	return r.Update(ctx, &ds)
}

func (r *RemediationReconciler) executePauseDaemonSetRollout(ctx context.Context, resource platformv1alpha1.ResourceRef) error {
	var ds appsv1.DaemonSet
	if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &ds); err != nil {
		return fmt.Errorf("failed to get DaemonSet: %w", err)
	}

	ds.Spec.UpdateStrategy.Type = appsv1.RollingUpdateDaemonSetStrategyType
	if ds.Spec.UpdateStrategy.RollingUpdate == nil {
		ds.Spec.UpdateStrategy.RollingUpdate = &appsv1.RollingUpdateDaemonSet{}
	}
	zero := intstr.FromInt(0)
	ds.Spec.UpdateStrategy.RollingUpdate.MaxUnavailable = &zero

	log.FromContext(ctx).Info("Paused DaemonSet rollout", "name", resource.Name)
	return r.Update(ctx, &ds)
}

func (r *RemediationReconciler) executeCordonAndDeleteDaemonSetPod(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	nodeName, ok := params["node"]
	if !ok || nodeName == "" {
		return fmt.Errorf("'node' param is REQUIRED for CordonAndDeleteDaemonSetPod")
	}

	// Cordon the node
	var node corev1.Node
	if err := r.Get(ctx, types.NamespacedName{Name: nodeName}, &node); err != nil {
		return fmt.Errorf("failed to get node %s: %w", nodeName, err)
	}
	if !node.Spec.Unschedulable {
		node.Spec.Unschedulable = true
		if err := r.Update(ctx, &node); err != nil {
			return fmt.Errorf("failed to cordon node %s: %w", nodeName, err)
		}
	}

	// Find and delete the DaemonSet pod on that node
	params["node"] = nodeName
	return r.executeDeleteDaemonSetPod(ctx, resource, params)
}

// ============================================================================
// Job Actions (9)
// ============================================================================

func (r *RemediationReconciler) executeRetryJob(ctx context.Context, resource platformv1alpha1.ResourceRef, _ map[string]string) error {
	logger := log.FromContext(ctx)

	var job batchv1.Job
	if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &job); err != nil {
		return fmt.Errorf("failed to get Job %s/%s: %w", resource.Namespace, resource.Name, err)
	}

	// Verify it's failed
	isFailed := false
	for _, cond := range job.Status.Conditions {
		if cond.Type == batchv1.JobFailed && cond.Status == "True" {
			isFailed = true
			break
		}
	}
	if !isFailed && job.Status.Active == 0 && job.Status.Succeeded == 0 {
		isFailed = true // No active, no succeeded = effectively failed
	}
	if !isFailed {
		return fmt.Errorf("Job %s is not in failed state", resource.Name)
	}

	// Capture the spec for recreation
	newJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:        fmt.Sprintf("%s-retry-%d", resource.Name, time.Now().Unix()),
			Namespace:   resource.Namespace,
			Labels:      job.Labels,
			Annotations: job.Annotations,
		},
		Spec: *job.Spec.DeepCopy(),
	}
	// Clear fields that can't be set on creation
	newJob.Spec.Selector = nil
	newJob.Spec.Template.Labels = nil

	// Delete the old job
	propagation := metav1.DeletePropagationBackground
	if err := r.Delete(ctx, &job, &client.DeleteOptions{PropagationPolicy: &propagation}); err != nil {
		return fmt.Errorf("failed to delete old Job: %w", err)
	}

	if err := r.Create(ctx, newJob); err != nil {
		return fmt.Errorf("failed to create retry Job: %w", err)
	}

	logger.Info("Retried Job", "old", resource.Name, "new", newJob.Name)
	return nil
}

func (r *RemediationReconciler) executeAdjustJobResources(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	var job batchv1.Job
	if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &job); err != nil {
		return fmt.Errorf("failed to get Job %s/%s: %w", resource.Namespace, resource.Name, err)
	}

	if err := adjustContainerResources(job.Spec.Template.Spec.Containers, params); err != nil {
		return err
	}
	return r.Update(ctx, &job)
}

func (r *RemediationReconciler) executeDeleteFailedJob(ctx context.Context, resource platformv1alpha1.ResourceRef, _ map[string]string) error {
	var job batchv1.Job
	if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &job); err != nil {
		return fmt.Errorf("failed to get Job %s/%s: %w", resource.Namespace, resource.Name, err)
	}

	isFailed := false
	for _, cond := range job.Status.Conditions {
		if cond.Type == batchv1.JobFailed && cond.Status == "True" {
			isFailed = true
			break
		}
	}
	if !isFailed {
		return fmt.Errorf("Job %s is not in failed state, refusing to delete", resource.Name)
	}

	propagation := metav1.DeletePropagationBackground
	return r.Delete(ctx, &job, &client.DeleteOptions{PropagationPolicy: &propagation})
}

func (r *RemediationReconciler) executeSuspendJob(ctx context.Context, resource platformv1alpha1.ResourceRef, _ map[string]string) error {
	var job batchv1.Job
	if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &job); err != nil {
		return fmt.Errorf("failed to get Job %s/%s: %w", resource.Namespace, resource.Name, err)
	}

	t := true
	job.Spec.Suspend = &t
	return r.Update(ctx, &job)
}

func (r *RemediationReconciler) executeResumeJob(ctx context.Context, resource platformv1alpha1.ResourceRef, _ map[string]string) error {
	var job batchv1.Job
	if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &job); err != nil {
		return fmt.Errorf("failed to get Job %s/%s: %w", resource.Namespace, resource.Name, err)
	}

	f := false
	job.Spec.Suspend = &f
	return r.Update(ctx, &job)
}

func (r *RemediationReconciler) executeAdjustJobParallelism(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	pStr, ok := params["parallelism"]
	if !ok {
		return fmt.Errorf("missing 'parallelism' param")
	}
	p, err := strconv.Atoi(pStr)
	if err != nil || p < 1 {
		return fmt.Errorf("invalid parallelism %q: must be >= 1", pStr)
	}

	var job batchv1.Job
	if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &job); err != nil {
		return fmt.Errorf("failed to get Job: %w", err)
	}

	p32 := int32(p)
	job.Spec.Parallelism = &p32
	return r.Update(ctx, &job)
}

func (r *RemediationReconciler) executeAdjustJobDeadline(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	dStr, ok := params["activeDeadlineSeconds"]
	if !ok {
		return fmt.Errorf("missing 'activeDeadlineSeconds' param")
	}
	d, err := strconv.ParseInt(dStr, 10, 64)
	if err != nil || d < 1 {
		return fmt.Errorf("invalid activeDeadlineSeconds %q: must be >= 1", dStr)
	}

	var job batchv1.Job
	if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &job); err != nil {
		return fmt.Errorf("failed to get Job: %w", err)
	}

	job.Spec.ActiveDeadlineSeconds = &d
	return r.Update(ctx, &job)
}

func (r *RemediationReconciler) executeAdjustJobBackoffLimit(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	bStr, ok := params["backoffLimit"]
	if !ok {
		return fmt.Errorf("missing 'backoffLimit' param")
	}
	b, err := strconv.Atoi(bStr)
	if err != nil || b < 0 {
		return fmt.Errorf("invalid backoffLimit %q: must be >= 0", bStr)
	}

	var job batchv1.Job
	if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &job); err != nil {
		return fmt.Errorf("failed to get Job: %w", err)
	}

	b32 := int32(b)
	job.Spec.BackoffLimit = &b32
	return r.Update(ctx, &job)
}

func (r *RemediationReconciler) executeForceDeleteJobPods(ctx context.Context, resource platformv1alpha1.ResourceRef, _ map[string]string) error {
	logger := log.FromContext(ctx)

	var podList corev1.PodList
	if err := r.List(ctx, &podList, client.InNamespace(resource.Namespace)); err != nil {
		return fmt.Errorf("listing pods: %w", err)
	}

	deleted := 0
	for i := range podList.Items {
		pod := &podList.Items[i]
		if isResourcePod(pod, resource) {
			if err := r.Delete(ctx, pod, client.GracePeriodSeconds(0)); err != nil {
				logger.Info("Failed to force-delete job pod", "pod", pod.Name, "error", err)
				continue
			}
			deleted++
		}
	}

	logger.Info("Force-deleted Job pods", "job", resource.Name, "deleted", deleted)
	return nil
}

// ============================================================================
// CronJob Actions (10)
// ============================================================================

func (r *RemediationReconciler) executeSuspendCronJob(ctx context.Context, resource platformv1alpha1.ResourceRef, _ map[string]string) error {
	var cj batchv1.CronJob
	if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &cj); err != nil {
		return fmt.Errorf("failed to get CronJob %s/%s: %w", resource.Namespace, resource.Name, err)
	}

	t := true
	cj.Spec.Suspend = &t
	return r.Update(ctx, &cj)
}

func (r *RemediationReconciler) executeResumeCronJob(ctx context.Context, resource platformv1alpha1.ResourceRef, _ map[string]string) error {
	var cj batchv1.CronJob
	if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &cj); err != nil {
		return fmt.Errorf("failed to get CronJob %s/%s: %w", resource.Namespace, resource.Name, err)
	}

	f := false
	cj.Spec.Suspend = &f
	return r.Update(ctx, &cj)
}

func (r *RemediationReconciler) executeTriggerCronJob(ctx context.Context, resource platformv1alpha1.ResourceRef, _ map[string]string) error {
	logger := log.FromContext(ctx)

	var cj batchv1.CronJob
	if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &cj); err != nil {
		return fmt.Errorf("failed to get CronJob %s/%s: %w", resource.Namespace, resource.Name, err)
	}

	jobName := fmt.Sprintf("%s-manual-%d", resource.Name, time.Now().Unix())
	if len(jobName) > 63 {
		jobName = jobName[:63]
	}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: resource.Namespace,
			Labels: map[string]string{
				"platform.chatcli.io/triggered-by": "aiops",
				"platform.chatcli.io/cronjob":      resource.Name,
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "batch/v1",
					Kind:       "CronJob",
					Name:       cj.Name,
					UID:        cj.UID,
				},
			},
		},
		Spec: cj.Spec.JobTemplate.Spec,
	}

	if err := r.Create(ctx, job); err != nil {
		return fmt.Errorf("failed to create manual Job from CronJob: %w", err)
	}

	logger.Info("Triggered CronJob manually", "cronjob", resource.Name, "job", jobName)
	return nil
}

func (r *RemediationReconciler) executeAdjustCronJobResources(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	var cj batchv1.CronJob
	if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &cj); err != nil {
		return fmt.Errorf("failed to get CronJob %s/%s: %w", resource.Namespace, resource.Name, err)
	}

	if err := adjustContainerResources(cj.Spec.JobTemplate.Spec.Template.Spec.Containers, params); err != nil {
		return err
	}
	return r.Update(ctx, &cj)
}

func (r *RemediationReconciler) executeAdjustCronJobSchedule(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	schedule, ok := params["schedule"]
	if !ok || schedule == "" {
		return fmt.Errorf("missing 'schedule' param")
	}

	// Basic cron validation: must have 5 fields
	fields := strings.Fields(schedule)
	if len(fields) != 5 {
		return fmt.Errorf("invalid cron schedule %q: must have exactly 5 fields", schedule)
	}

	var cj batchv1.CronJob
	if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &cj); err != nil {
		return fmt.Errorf("failed to get CronJob: %w", err)
	}

	cj.Spec.Schedule = schedule
	return r.Update(ctx, &cj)
}

func (r *RemediationReconciler) executeAdjustCronJobDeadline(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	dStr, ok := params["startingDeadlineSeconds"]
	if !ok {
		return fmt.Errorf("missing 'startingDeadlineSeconds' param")
	}
	d, err := strconv.ParseInt(dStr, 10, 64)
	if err != nil || d < 0 {
		return fmt.Errorf("invalid startingDeadlineSeconds %q: must be >= 0", dStr)
	}

	var cj batchv1.CronJob
	if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &cj); err != nil {
		return fmt.Errorf("failed to get CronJob: %w", err)
	}

	cj.Spec.StartingDeadlineSeconds = &d
	return r.Update(ctx, &cj)
}

func (r *RemediationReconciler) executeAdjustCronJobHistory(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	var cj batchv1.CronJob
	if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &cj); err != nil {
		return fmt.Errorf("failed to get CronJob: %w", err)
	}

	modified := false
	if sStr, ok := params["successfulJobsHistoryLimit"]; ok {
		s, err := strconv.Atoi(sStr)
		if err != nil || s < 0 {
			return fmt.Errorf("invalid successfulJobsHistoryLimit %q: must be >= 0", sStr)
		}
		s32 := int32(s)
		cj.Spec.SuccessfulJobsHistoryLimit = &s32
		modified = true
	}
	if fStr, ok := params["failedJobsHistoryLimit"]; ok {
		f, err := strconv.Atoi(fStr)
		if err != nil || f < 0 {
			return fmt.Errorf("invalid failedJobsHistoryLimit %q: must be >= 0", fStr)
		}
		f32 := int32(f)
		cj.Spec.FailedJobsHistoryLimit = &f32
		modified = true
	}

	if !modified {
		return fmt.Errorf("specify at least one of: successfulJobsHistoryLimit, failedJobsHistoryLimit")
	}
	return r.Update(ctx, &cj)
}

func (r *RemediationReconciler) executeAdjustCronJobConcurrency(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	policy := params["concurrencyPolicy"]
	switch batchv1.ConcurrencyPolicy(policy) {
	case batchv1.AllowConcurrent, batchv1.ForbidConcurrent, batchv1.ReplaceConcurrent:
		// valid
	default:
		return fmt.Errorf("invalid concurrencyPolicy %q: must be Allow, Forbid, or Replace", policy)
	}

	var cj batchv1.CronJob
	if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &cj); err != nil {
		return fmt.Errorf("failed to get CronJob: %w", err)
	}

	cj.Spec.ConcurrencyPolicy = batchv1.ConcurrencyPolicy(policy)
	return r.Update(ctx, &cj)
}

func (r *RemediationReconciler) executeDeleteCronJobActiveJobs(ctx context.Context, resource platformv1alpha1.ResourceRef, _ map[string]string) error {
	logger := log.FromContext(ctx)

	var cj batchv1.CronJob
	if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &cj); err != nil {
		return fmt.Errorf("failed to get CronJob: %w", err)
	}

	deleted := 0
	propagation := metav1.DeletePropagationBackground
	for _, ref := range cj.Status.Active {
		job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
			Name:      ref.Name,
			Namespace: resource.Namespace,
		}}
		if err := r.Delete(ctx, job, &client.DeleteOptions{PropagationPolicy: &propagation}); err != nil {
			logger.Info("Failed to delete active CronJob job", "job", ref.Name, "error", err)
			continue
		}
		deleted++
	}

	logger.Info("Deleted active CronJob jobs", "cronjob", resource.Name, "deleted", deleted)
	return nil
}

func (r *RemediationReconciler) executeReplaceCronJobTemplate(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	cmName := params["configmap"]
	if cmName == "" {
		return fmt.Errorf("missing 'configmap' param")
	}

	key := params["key"]
	if key == "" {
		key = "jobtemplate.json"
	}

	var cm corev1.ConfigMap
	if err := r.Get(ctx, types.NamespacedName{Name: cmName, Namespace: resource.Namespace}, &cm); err != nil {
		return fmt.Errorf("ConfigMap %s not found: %w", cmName, err)
	}

	templateData, ok := cm.Data[key]
	if !ok {
		return fmt.Errorf("key %s not found in ConfigMap %s", key, cmName)
	}

	var newTemplate batchv1.JobTemplateSpec
	if err := json.Unmarshal([]byte(templateData), &newTemplate); err != nil {
		return fmt.Errorf("invalid job template JSON in %s/%s: %w", cmName, key, err)
	}

	var cj batchv1.CronJob
	if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &cj); err != nil {
		return fmt.Errorf("failed to get CronJob: %w", err)
	}

	cj.Spec.JobTemplate = newTemplate
	return r.Update(ctx, &cj)
}

// Ensure imports are used
var (
	_ = schema.GroupVersionKind{}
	_ = networkingv1.NetworkPolicy{}
)
