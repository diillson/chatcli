/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// mountedConfigMapsHashAnnotation carries the digest of the ConfigMaps the
// server pod mounts as files (MCP server list, agents, skills). The server
// reads them once at startup, so an edit only takes effect after a
// rollout; the kubelet refreshing the mounted files is not enough.
const mountedConfigMapsHashAnnotation = "chatcli.io/mounted-configmaps-hash"

// mountedConfigMapNames lists, in a stable order, the ConfigMaps the
// Instance pod mounts as files and does not hash elsewhere. The watch
// config is left out: it is rendered from the spec and hashed from it.
func mountedConfigMapNames(inst *platformv1alpha1.Instance) []string {
	var names []string
	if mcp := inst.Spec.MCP; mcp != nil && mcp.Enabled {
		if mcp.ExistingConfigMap != "" {
			names = append(names, mcp.ExistingConfigMap)
		} else {
			names = append(names, inst.Name+"-mcp")
		}
	}
	if a := inst.Spec.Agents; a != nil {
		if a.ConfigMapRef != nil && *a.ConfigMapRef != "" {
			names = append(names, *a.ConfigMapRef)
		}
		if a.SkillsConfigMapRef != nil && *a.SkillsConfigMapRef != "" {
			names = append(names, *a.SkillsConfigMapRef)
		}
	}
	return names
}

// hashMountedConfigMaps digests the content of each named ConfigMap in the
// order given. A missing ConfigMap hashes as absent, so creating it later
// rolls the pods (the pod cannot start while a non-optional ConfigMap
// volume is missing, and restarts once it appears). Any other read error
// is returned so a transient failure never triggers a rollout on its own.
func hashMountedConfigMaps(ctx context.Context, c client.Client, namespace string, names []string) (string, error) {
	if len(names) == 0 {
		return "", nil
	}
	h := sha256.New()
	for _, name := range names {
		_, _ = fmt.Fprintf(h, "configmap %q\n", name)
		var cm corev1.ConfigMap
		if err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &cm); err != nil {
			if errors.IsNotFound(err) {
				_, _ = fmt.Fprint(h, "absent\n")
				continue
			}
			return "", fmt.Errorf("reading configmap %q: %w", name, err)
		}
		keys := make([]string, 0, len(cm.Data))
		for k := range cm.Data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			sum := sha256.Sum256([]byte(cm.Data[k]))
			_, _ = fmt.Fprintf(h, "data %q %x\n", k, sum)
		}
		binKeys := make([]string, 0, len(cm.BinaryData))
		for k := range cm.BinaryData {
			binKeys = append(binKeys, k)
		}
		sort.Strings(binKeys)
		for _, k := range binKeys {
			sum := sha256.Sum256(cm.BinaryData[k])
			_, _ = fmt.Fprintf(h, "binary %q %x\n", k, sum)
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil))[:16], nil
}

// configMapToInstance maps a ConfigMap event to the Instances that mount
// it as files. The Instance's own ConfigMaps are already covered by Owns;
// this catches the user-managed ones (mcp.existingConfigMap, agents and
// skills), whose edits would otherwise wait for an unrelated reconcile.
func (r *InstanceReconciler) configMapToInstance(ctx context.Context, obj client.Object) []reconcile.Request {
	var instances platformv1alpha1.InstanceList
	if err := r.List(ctx, &instances, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}
	var requests []reconcile.Request
	for i := range instances.Items {
		inst := &instances.Items[i]
		for _, name := range mountedConfigMapNames(inst) {
			if name == obj.GetName() {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: inst.Name, Namespace: inst.Namespace},
				})
				break
			}
		}
	}
	return requests
}
