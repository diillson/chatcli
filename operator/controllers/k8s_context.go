package controllers

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

const maxContextChars = 12000

// KubernetesContextBuilder collects real cluster data for AI analysis enrichment.
type KubernetesContextBuilder struct {
	client    client.Client
	clientset kubernetes.Interface // for pod logs subresource
}

// NewKubernetesContextBuilder creates a new context builder.
func NewKubernetesContextBuilder(c client.Client, clientset ...kubernetes.Interface) *KubernetesContextBuilder {
	b := &KubernetesContextBuilder{client: c}
	if len(clientset) > 0 && clientset[0] != nil {
		b.clientset = clientset[0]
	}
	return b
}

// BuildContext collects deployment status, pod details, pod logs, events, and revision history
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

	// 3. Pod Logs (unhealthy pods only)
	if b.clientset != nil {
		logCtx, err := b.buildPodLogs(ctx, resource)
		if err != nil {
			sb.WriteString(fmt.Sprintf("## Pod Logs\nError fetching logs: %v\n\n", err))
		} else if logCtx != "" {
			sb.WriteString(logCtx)
		}
	}

	// 4. Recent Events
	eventCtx, err := b.buildRecentEvents(ctx, resource)
	if err != nil {
		sb.WriteString(fmt.Sprintf("## Recent Events\nError fetching events: %v\n\n", err))
	} else {
		sb.WriteString(eventCtx)
	}

	// 5. Revision History
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
		if len(c.Command) > 0 {
			sb.WriteString(fmt.Sprintf(" command=%v", c.Command))
		}
		if len(c.Args) > 0 {
			sb.WriteString(fmt.Sprintf(" args=%v", c.Args))
		}
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

// buildPodLogs fetches recent logs from unhealthy pods for root cause analysis.
func (b *KubernetesContextBuilder) buildPodLogs(ctx context.Context, resource platformv1alpha1.ResourceRef) (string, error) {
	var podList corev1.PodList
	if err := b.client.List(ctx, &podList, client.InNamespace(resource.Namespace)); err != nil {
		return "", err
	}

	// Filter unhealthy pods owned by this deployment
	var unhealthy []corev1.Pod
	for i := range podList.Items {
		pod := &podList.Items[i]
		if !isPodOwnedByDeployment(pod, resource.Name) {
			continue
		}
		if !isPodReady(pod) || podRestartCount(pod) > 0 {
			unhealthy = append(unhealthy, *pod)
		}
	}

	if len(unhealthy) == 0 {
		return "", nil
	}

	// Sort by restart count descending, limit to 3
	sort.Slice(unhealthy, func(i, j int) bool {
		return podRestartCount(&unhealthy[i]) > podRestartCount(&unhealthy[j])
	})
	if len(unhealthy) > 3 {
		unhealthy = unhealthy[:3]
	}

	var sb strings.Builder
	sb.WriteString("## Pod Logs (unhealthy pods)\n")

	tailLines := int64(50)
	for _, pod := range unhealthy {
		for _, cs := range pod.Status.ContainerStatuses {
			// Fetch current container logs
			logs := b.fetchContainerLogs(ctx, pod.Name, cs.Name, resource.Namespace, tailLines, false)
			if logs != "" {
				sb.WriteString(fmt.Sprintf("### %s/%s (restarts=%d)\n%s\n", pod.Name, cs.Name, cs.RestartCount, logs))
			}

			// Fetch previous container logs if there were restarts
			if cs.RestartCount > 0 {
				prevLogs := b.fetchContainerLogs(ctx, pod.Name, cs.Name, resource.Namespace, tailLines, true)
				if prevLogs != "" {
					sb.WriteString(fmt.Sprintf("### %s/%s (previous terminated instance)\n%s\n", pod.Name, cs.Name, prevLogs))
				}
			}
		}
	}

	if sb.Len() == len("## Pod Logs (unhealthy pods)\n") {
		return "", nil
	}
	sb.WriteString("\n")
	return sb.String(), nil
}

func (b *KubernetesContextBuilder) fetchContainerLogs(ctx context.Context, podName, containerName, namespace string, tailLines int64, previous bool) string {
	req := b.clientset.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{
		Container: containerName,
		TailLines: &tailLines,
		Previous:  previous,
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		return ""
	}
	defer stream.Close()

	data, err := io.ReadAll(io.LimitReader(stream, 8192))
	if err != nil {
		return ""
	}
	return string(data)
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

		// Show spec diff with previous revision
		if i < len(owned)-1 {
			diffs := diffContainerSpecs(
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

// diffContainerSpecs compares two container specs and returns descriptions of changes
// in images, commands, args, and resources between revisions.
func diffContainerSpecs(current, previous []corev1.Container) []string {
	type containerInfo struct {
		Image    string
		Command  []string
		Args     []string
		CPUReq   string
		MemReq   string
		CPULimit string
		MemLimit string
	}

	extractInfo := func(c corev1.Container) containerInfo {
		info := containerInfo{
			Image:   c.Image,
			Command: c.Command,
			Args:    c.Args,
		}
		if c.Resources.Requests != nil {
			info.CPUReq = c.Resources.Requests.Cpu().String()
			info.MemReq = c.Resources.Requests.Memory().String()
		}
		if c.Resources.Limits != nil {
			info.CPULimit = c.Resources.Limits.Cpu().String()
			info.MemLimit = c.Resources.Limits.Memory().String()
		}
		return info
	}

	prevMap := make(map[string]containerInfo, len(previous))
	for _, c := range previous {
		prevMap[c.Name] = extractInfo(c)
	}

	var diffs []string
	for _, c := range current {
		cur := extractInfo(c)
		prev, ok := prevMap[c.Name]
		if !ok {
			diffs = append(diffs, fmt.Sprintf("%s: (new container) image=%s", c.Name, cur.Image))
			continue
		}
		if prev.Image != cur.Image {
			diffs = append(diffs, fmt.Sprintf("%s image: %s → %s", c.Name, prev.Image, cur.Image))
		}
		if fmt.Sprint(prev.Command) != fmt.Sprint(cur.Command) {
			diffs = append(diffs, fmt.Sprintf("%s command: %v → %v", c.Name, prev.Command, cur.Command))
		}
		if fmt.Sprint(prev.Args) != fmt.Sprint(cur.Args) {
			diffs = append(diffs, fmt.Sprintf("%s args: %v → %v", c.Name, prev.Args, cur.Args))
		}
		if prev.CPUReq != cur.CPUReq || prev.MemReq != cur.MemReq {
			diffs = append(diffs, fmt.Sprintf("%s requests: [cpu=%s mem=%s] → [cpu=%s mem=%s]",
				c.Name, prev.CPUReq, prev.MemReq, cur.CPUReq, cur.MemReq))
		}
		if prev.CPULimit != cur.CPULimit || prev.MemLimit != cur.MemLimit {
			diffs = append(diffs, fmt.Sprintf("%s limits: [cpu=%s mem=%s] → [cpu=%s mem=%s]",
				c.Name, prev.CPULimit, prev.MemLimit, cur.CPULimit, cur.MemLimit))
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
