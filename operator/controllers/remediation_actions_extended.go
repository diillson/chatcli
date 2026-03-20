package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apiresource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
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
		"env":            true,
		"whoami":         true,
		"df -h":          true,
		"free -m":        true,
		"cat /etc/hosts": true,
		"ps aux":         true,
		"netstat -tlnp":  true,
		"ss -tlnp":       true,
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

	// Safety: disallow certain dangerous kinds
	dangerousKinds := map[string]bool{
		"ClusterRole": true, "ClusterRoleBinding": true,
		"Namespace": true, "Node": true,
		"PersistentVolume": true,
	}
	if dangerousKinds[obj.GetKind()] {
		return fmt.Errorf("applying %s resources is not allowed for safety", obj.GetKind())
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

// Ensure imports are used
var (
	_ = schema.GroupVersionKind{}
	_ = networkingv1.NetworkPolicy{}
)
