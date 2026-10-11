/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// nodeActions are the action types that act on a whole node named by the
// "node" param.
var nodeActions = map[platformv1alpha1.RemediationActionType]bool{
	platformv1alpha1.ActionCordonNode:                  true,
	platformv1alpha1.ActionUncordonNode:                true,
	platformv1alpha1.ActionDrainNode:                   true,
	platformv1alpha1.ActionCordonAndDeleteDaemonSetPod: true,
}

// ensureNodeBoundToResource refuses a node action whose node has nothing to
// do with the Issue: the node must be the Issue's resource itself (a Node
// Issue), or host a pod the Issue's workload owns. Without it, a plan, or
// an AI proposal, could cordon or drain any node in the cluster.
func (r *RemediationReconciler) ensureNodeBoundToResource(ctx context.Context, resource platformv1alpha1.ResourceRef, nodeName string) error {
	nodeName = strings.TrimSpace(nodeName)
	if nodeName == "" {
		return fmt.Errorf("missing 'node' param")
	}
	if resource.Kind == "Node" {
		if resource.Name == nodeName {
			return nil
		}
		return fmt.Errorf("node %s is not the Issue's node %s", nodeName, resource.Name)
	}
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(resource.Namespace)); err != nil {
		return fmt.Errorf("checking node %s against %s %s/%s: %w", nodeName, resource.Kind, resource.Namespace, resource.Name, err)
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Spec.NodeName != nodeName {
			continue
		}
		owned, err := podOwnedByResource(ctx, r.Client, pod, resource)
		if err != nil {
			return fmt.Errorf("checking node %s against %s %s/%s: %w", nodeName, resource.Kind, resource.Namespace, resource.Name, err)
		}
		if owned {
			return nil
		}
	}
	return fmt.Errorf("node %s hosts no pod of %s %s/%s; node actions may only target a node running the Issue's workload",
		nodeName, resource.Kind, resource.Namespace, resource.Name)
}

// podOwnedByResource follows the pod's controller references up to the
// workload: ReplicaSet to Deployment, Job to CronJob, or directly to a
// StatefulSet, DaemonSet or Job. Names alone are not trusted: a prefix
// match would let "web" claim the pods of "web-admin".
func podOwnedByResource(ctx context.Context, c client.Reader, pod *corev1.Pod, resource platformv1alpha1.ResourceRef) (bool, error) {
	owner := metav1.GetControllerOf(pod)
	if owner == nil {
		return false, nil
	}
	key := func(name string) types.NamespacedName {
		return types.NamespacedName{Namespace: pod.Namespace, Name: name}
	}
	switch resource.Kind {
	case "StatefulSet", "DaemonSet", "Job":
		return owner.Kind == resource.Kind && owner.Name == resource.Name, nil
	case "Deployment":
		if owner.Kind != "ReplicaSet" {
			return false, nil
		}
		var rs appsv1.ReplicaSet
		if err := c.Get(ctx, key(owner.Name), &rs); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		up := metav1.GetControllerOf(&rs)
		return up != nil && up.Kind == "Deployment" && up.Name == resource.Name, nil
	case "CronJob":
		if owner.Kind != "Job" {
			return false, nil
		}
		var job batchv1.Job
		if err := c.Get(ctx, key(owner.Name), &job); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		up := metav1.GetControllerOf(&job)
		return up != nil && up.Kind == "CronJob" && up.Name == resource.Name, nil
	}
	return false, nil
}
