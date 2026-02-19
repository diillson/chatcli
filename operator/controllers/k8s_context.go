package controllers

import (
	"context"
	"fmt"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

const maxContextChars = 8000

// KubernetesContextBuilder collects real cluster data for AI analysis enrichment.
type KubernetesContextBuilder struct {
	client client.Client
}

// NewKubernetesContextBuilder creates a new context builder.
func NewKubernetesContextBuilder(c client.Client) *KubernetesContextBuilder {
	return &KubernetesContextBuilder{client: c}
}

// BuildContext collects deployment status, pod details, events, and revision history
// for the given resource reference. Returns a formatted text suitable for inclusion in
// AI analysis prompts.
func (b *KubernetesContextBuilder) BuildContext(ctx context.Context, resource platformv1alpha1.ResourceRef) (string, error) {
	if resource.Kind != "Deployment" {
		return fmt.Sprintf("Resource kind %q — context collection only supports Deployments.", resource.Kind), nil
	}

	var sb strings.Builder

	// 1. Deployment Status
	deployCtx, err := b.buildDeploymentStatus(ctx, resource)
	if err != nil {
		sb.WriteString(fmt.Sprintf("## Deployment Status\nError fetching deployment: %v\n\n", err))
	} else {
		sb.WriteString(deployCtx)
	}

	// 2. Pod Details
	podCtx, err := b.buildPodDetails(ctx, resource)
	if err != nil {
		sb.WriteString(fmt.Sprintf("## Pod Details\nError fetching pods: %v\n\n", err))
	} else {
		sb.WriteString(podCtx)
	}

	// 3. Recent Events
	eventCtx, err := b.buildRecentEvents(ctx, resource)
	if err != nil {
		sb.WriteString(fmt.Sprintf("## Recent Events\nError fetching events: %v\n\n", err))
	} else {
		sb.WriteString(eventCtx)
	}

	// 4. Revision History
	revCtx, err := b.buildRevisionHistory(ctx, resource)
	if err != nil {
		sb.WriteString(fmt.Sprintf("## Revision History\nError fetching replicasets: %v\n\n", err))
	} else {
		sb.WriteString(revCtx)
	}

	result := sb.String()
	if len(result) > maxContextChars {
		result = result[:maxContextChars-3] + "..."
	}
	return result, nil
}

func (b *KubernetesContextBuilder) buildDeploymentStatus(ctx context.Context, resource platformv1alpha1.ResourceRef) (string, error) {
	var deploy appsv1.Deployment
	if err := b.client.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &deploy); err != nil {
		return "", err
	}

	desired := int32(1)
	if deploy.Spec.Replicas != nil {
		desired = *deploy.Spec.Replicas
	}

	var sb strings.Builder
	sb.WriteString("## Deployment Status\n")
	sb.WriteString(fmt.Sprintf("Name: %s/%s\n", deploy.Namespace, deploy.Name))
	sb.WriteString(fmt.Sprintf("Replicas: desired=%d ready=%d updated=%d available=%d unavailable=%d\n",
		desired, deploy.Status.ReadyReplicas, deploy.Status.UpdatedReplicas,
		deploy.Status.AvailableReplicas, deploy.Status.UnavailableReplicas))
	sb.WriteString(fmt.Sprintf("Generation: %d (observed: %d)\n", deploy.Generation, deploy.Status.ObservedGeneration))

	// Containers info
	for _, c := range deploy.Spec.Template.Spec.Containers {
		sb.WriteString(fmt.Sprintf("Container: %s image=%s", c.Name, c.Image))
		if c.Resources.Requests != nil {
			sb.WriteString(fmt.Sprintf(" requests=[cpu=%s mem=%s]",
				c.Resources.Requests.Cpu().String(),
				c.Resources.Requests.Memory().String()))
		}
		if c.Resources.Limits != nil {
			sb.WriteString(fmt.Sprintf(" limits=[cpu=%s mem=%s]",
				c.Resources.Limits.Cpu().String(),
				c.Resources.Limits.Memory().String()))
		}
		sb.WriteString("\n")
	}

	// Conditions
	for _, cond := range deploy.Status.Conditions {
		sb.WriteString(fmt.Sprintf("Condition: %s=%s reason=%s message=%q\n",
			cond.Type, cond.Status, cond.Reason, cond.Message))
	}
	sb.WriteString("\n")

	return sb.String(), nil
}

func (b *KubernetesContextBuilder) buildPodDetails(ctx context.Context, resource platformv1alpha1.ResourceRef) (string, error) {
	var podList corev1.PodList
	if err := b.client.List(ctx, &podList, client.InNamespace(resource.Namespace)); err != nil {
		return "", err
	}

	// Filter pods owned by this deployment (via ReplicaSet)
	var matchingPods []corev1.Pod
	for i := range podList.Items {
		if isPodOwnedByDeployment(&podList.Items[i], resource.Name) {
			matchingPods = append(matchingPods, podList.Items[i])
		}
	}

	if len(matchingPods) == 0 {
		return "## Pod Details\nNo pods found for deployment.\n\n", nil
	}

	// Limit to 5 pods, prioritize unhealthy
	sort.Slice(matchingPods, func(i, j int) bool {
		iReady := isPodReady(&matchingPods[i])
		jReady := isPodReady(&matchingPods[j])
		if iReady != jReady {
			return !iReady // unhealthy first
		}
		// Then by restart count descending
		return podRestartCount(&matchingPods[i]) > podRestartCount(&matchingPods[j])
	})
	if len(matchingPods) > 5 {
		matchingPods = matchingPods[:5]
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## Pod Details (showing %d pods)\n", len(matchingPods)))

	for _, pod := range matchingPods {
		sb.WriteString(fmt.Sprintf("Pod: %s phase=%s\n", pod.Name, pod.Status.Phase))
		for _, cs := range pod.Status.ContainerStatuses {
			sb.WriteString(fmt.Sprintf("  Container: %s ready=%t restarts=%d\n",
				cs.Name, cs.Ready, cs.RestartCount))
			if cs.State.Waiting != nil {
				sb.WriteString(fmt.Sprintf("    State: Waiting reason=%s message=%q\n",
					cs.State.Waiting.Reason, cs.State.Waiting.Message))
			}
			if cs.State.Terminated != nil {
				sb.WriteString(fmt.Sprintf("    State: Terminated reason=%s exitCode=%d\n",
					cs.State.Terminated.Reason, cs.State.Terminated.ExitCode))
			}
			if cs.LastTerminationState.Terminated != nil {
				t := cs.LastTerminationState.Terminated
				sb.WriteString(fmt.Sprintf("    LastTermination: reason=%s exitCode=%d\n",
					t.Reason, t.ExitCode))
			}
		}
	}
	sb.WriteString("\n")

	return sb.String(), nil
}

func (b *KubernetesContextBuilder) buildRecentEvents(ctx context.Context, resource platformv1alpha1.ResourceRef) (string, error) {
	var eventList corev1.EventList
	if err := b.client.List(ctx, &eventList, client.InNamespace(resource.Namespace)); err != nil {
		return "", err
	}

	// Filter events related to the deployment or its pods
	var relevant []corev1.Event
	for i := range eventList.Items {
		ev := &eventList.Items[i]
		if ev.InvolvedObject.Name == resource.Name ||
			strings.HasPrefix(ev.InvolvedObject.Name, resource.Name+"-") {
			relevant = append(relevant, *ev)
		}
	}

	// Sort by last timestamp descending
	sort.Slice(relevant, func(i, j int) bool {
		ti := relevant[i].LastTimestamp.Time
		tj := relevant[j].LastTimestamp.Time
		if ti.IsZero() {
			ti = relevant[i].CreationTimestamp.Time
		}
		if tj.IsZero() {
			tj = relevant[j].CreationTimestamp.Time
		}
		return ti.After(tj)
	})

	if len(relevant) > 15 {
		relevant = relevant[:15]
	}

	if len(relevant) == 0 {
		return "## Recent Events\nNo events found.\n\n", nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## Recent Events (last %d)\n", len(relevant)))
	for _, ev := range relevant {
		sb.WriteString(fmt.Sprintf("%s %s: %s (count=%d object=%s)\n",
			ev.Type, ev.Reason, ev.Message, ev.Count, ev.InvolvedObject.Name))
	}
	sb.WriteString("\n")

	return sb.String(), nil
}

func (b *KubernetesContextBuilder) buildRevisionHistory(ctx context.Context, resource platformv1alpha1.ResourceRef) (string, error) {
	var rsList appsv1.ReplicaSetList
	if err := b.client.List(ctx, &rsList, client.InNamespace(resource.Namespace)); err != nil {
		return "", err
	}

	// Filter ReplicaSets owned by this deployment
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

	if len(owned) == 0 {
		return "## Revision History\nNo ReplicaSets found.\n\n", nil
	}

	// Sort by revision descending
	sort.Slice(owned, func(i, j int) bool {
		return owned[i].revision > owned[j].revision
	})

	// Keep last 5 revisions
	if len(owned) > 5 {
		owned = owned[:5]
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## Revision History (last %d revisions)\n", len(owned)))

	for i, info := range owned {
		replicas := int32(0)
		if info.rs.Spec.Replicas != nil {
			replicas = *info.rs.Spec.Replicas
		}
		sb.WriteString(fmt.Sprintf("Revision %d: replicas=%d/%d",
			info.revision, info.rs.Status.ReadyReplicas, replicas))

		// Show container images
		for _, c := range info.rs.Spec.Template.Spec.Containers {
			sb.WriteString(fmt.Sprintf(" [%s=%s]", c.Name, c.Image))
		}
		sb.WriteString("\n")

		// Show image diff with previous revision
		if i < len(owned)-1 {
			diffs := diffContainerImages(
				info.rs.Spec.Template.Spec.Containers,
				owned[i+1].rs.Spec.Template.Spec.Containers,
			)
			for _, d := range diffs {
				sb.WriteString(fmt.Sprintf("  Changed: %s\n", d))
			}
		}
	}
	sb.WriteString("\n")

	return sb.String(), nil
}

// isPodOwnedByDeployment checks if a pod's ownerReference chain goes through
// a ReplicaSet whose name starts with the deployment name.
func isPodOwnedByDeployment(pod *corev1.Pod, deployName string) bool {
	for _, ref := range pod.OwnerReferences {
		if ref.Kind == "ReplicaSet" && strings.HasPrefix(ref.Name, deployName+"-") {
			return true
		}
	}
	return false
}

func isPodReady(pod *corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func podRestartCount(pod *corev1.Pod) int32 {
	var total int32
	for _, cs := range pod.Status.ContainerStatuses {
		total += cs.RestartCount
	}
	return total
}

// diffContainerImages compares two container specs and returns descriptions of image changes.
func diffContainerImages(current, previous []corev1.Container) []string {
	prevMap := make(map[string]string, len(previous))
	for _, c := range previous {
		prevMap[c.Name] = c.Image
	}

	var diffs []string
	for _, c := range current {
		if prevImg, ok := prevMap[c.Name]; ok {
			if prevImg != c.Image {
				diffs = append(diffs, fmt.Sprintf("%s: %s → %s", c.Name, prevImg, c.Image))
			}
		} else {
			diffs = append(diffs, fmt.Sprintf("%s: (new container) %s", c.Name, c.Image))
		}
	}
	for _, c := range previous {
		found := false
		for _, cc := range current {
			if cc.Name == c.Name {
				found = true
				break
			}
		}
		if !found {
			diffs = append(diffs, fmt.Sprintf("%s: (removed)", c.Name))
		}
	}
	return diffs
}
