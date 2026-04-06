/*
 * ChatCLI - Kubernetes Operator
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package controllers

import (
	"fmt"
	"os"
	"strings"
	"sync"
)

// Cached singleton — initialized once, reused across reconciliations.
var (
	cachedAllowlist     *ResourceAllowlist
	cachedAllowlistOnce sync.Once
)

// getResourceAllowlist returns a cached ResourceAllowlist singleton.
func getResourceAllowlist() *ResourceAllowlist {
	cachedAllowlistOnce.Do(func() {
		cachedAllowlist = NewResourceAllowlist(os.Getenv("CHATCLI_ALLOWED_RESOURCE_TYPES"))
	})
	return cachedAllowlist
}

// ResourceAllowlist controls which Kubernetes resource types can be created/modified
// by remediation actions. Uses an allowlist approach (C5) — any resource type not
// explicitly permitted requires manual approval.
type ResourceAllowlist struct {
	mu             sync.RWMutex
	allowedTypes   map[string]bool
	dangerousTypes map[string]string // kind -> reason
	managedNSLabel string            // namespace label required for operations
}

// DefaultAllowedResourceTypes returns the resource types safe for automated remediation.
func DefaultAllowedResourceTypes() map[string]bool {
	return map[string]bool{
		"Deployment":              true,
		"StatefulSet":             true,
		"DaemonSet":               true,
		"ReplicaSet":              true,
		"Service":                 true,
		"ConfigMap":               true,
		"HorizontalPodAutoscaler": true,
		"PodDisruptionBudget":     true,
		"Ingress":                 true,
		"CronJob":                 true,
		"Job":                     true,
		"ServiceMonitor":          true,
		"PrometheusRule":          true,
		"PodMonitor":              true,
		"ServiceEntry":            true,
		"VirtualService":          true,
		"DestinationRule":         true,
	}
}

// DefaultDangerousResourceTypes returns types that require explicit approval with reasons.
func DefaultDangerousResourceTypes() map[string]string {
	return map[string]string{
		"ClusterRole":                    "can escalate cluster-wide permissions",
		"ClusterRoleBinding":             "can grant cluster-wide access to any subject",
		"Role":                           "can modify namespace-scoped permissions",
		"RoleBinding":                    "can grant namespace-scoped access",
		"Namespace":                      "can affect isolation boundaries",
		"Node":                           "can affect cluster node state",
		"PersistentVolume":               "can affect persistent storage across namespaces",
		"StorageClass":                   "can affect storage provisioning cluster-wide",
		"Secret":                         "contains sensitive credentials and keys",
		"ServiceAccount":                 "can be used for privilege escalation via token mounting",
		"NetworkPolicy":                  "can isolate or expose workloads to network traffic",
		"PodSecurityPolicy":              "can affect pod security constraints",
		"MutatingWebhookConfiguration":   "can intercept and modify all API requests",
		"ValidatingWebhookConfiguration": "can intercept and reject API requests",
		"CustomResourceDefinition":       "can extend the API surface",
		"PriorityClass":                  "can affect pod scheduling priority",
		"ResourceQuota":                  "can affect namespace resource limits",
		"LimitRange":                     "can affect pod resource defaults",
	}
}

// NewResourceAllowlist creates an allowlist with defaults and optional custom types
// from a comma-separated string (e.g., from ConfigMap key "allowed_resource_types").
func NewResourceAllowlist(additionalTypes string) *ResourceAllowlist {
	al := &ResourceAllowlist{
		allowedTypes:   DefaultAllowedResourceTypes(),
		dangerousTypes: DefaultDangerousResourceTypes(),
		managedNSLabel: "chatcli.io/managed",
	}

	// Add custom allowed types from configuration
	if additionalTypes != "" {
		for _, t := range strings.Split(additionalTypes, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				al.allowedTypes[t] = true
			}
		}
	}

	return al
}

// IsAllowed checks if a resource type is in the allowlist.
func (al *ResourceAllowlist) IsAllowed(kind string) bool {
	al.mu.RLock()
	defer al.mu.RUnlock()
	return al.allowedTypes[kind]
}

// IsDangerous checks if a resource type is in the dangerous list.
// Returns (isDangerous, reason).
func (al *ResourceAllowlist) IsDangerous(kind string) (bool, string) {
	al.mu.RLock()
	defer al.mu.RUnlock()
	reason, dangerous := al.dangerousTypes[kind]
	return dangerous, reason
}

// CheckResourceAccess validates whether a remediation can operate on the given resource type.
// Returns nil if allowed, or an error explaining why it's blocked.
func (al *ResourceAllowlist) CheckResourceAccess(kind string) error {
	if al.IsAllowed(kind) {
		return nil
	}

	if dangerous, reason := al.IsDangerous(kind); dangerous {
		return fmt.Errorf("resource type %q requires explicit approval: %s", kind, reason)
	}

	return fmt.Errorf("resource type %q is not in the allowed list; add it to 'allowed_resource_types' in operator config or create an ApprovalRequest", kind)
}

// GetManagedNamespaceLabel returns the label key used to identify managed namespaces.
func (al *ResourceAllowlist) GetManagedNamespaceLabel() string {
	return al.managedNSLabel
}
