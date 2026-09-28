/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// restrictedContainerSecurityContext is the container-level context every
// container of an Instance pod runs with. Together with the pod-level
// default (non-root, RuntimeDefault seccomp) it satisfies the restricted
// Pod Security Standard.
func restrictedContainerSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: boolPtr(false),
		ReadOnlyRootFilesystem:   boolPtr(true),
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
	}
}

// Kubelet probes for the server container.
//
// The server answers GET /healthz with 200 on its metrics port, which the
// operator always enables (metricsPort 0 still renders the default 9090):
// the endpoint and the --metrics-port flag the operator passes shipped
// together, so every image that accepts the operator's arguments serves
// it. It is plain HTTP whatever the gRPC TLS settings are, and it is not
// behind the gRPC authentication, so the probe needs no credential.
const serverHealthPath = "/healthz"

// metricsPortName is the named container port the probes target.
const metricsPortName = "metrics"

func serverHealthHandler() corev1.ProbeHandler {
	return corev1.ProbeHandler{
		HTTPGet: &corev1.HTTPGetAction{
			Path:   serverHealthPath,
			Port:   intstr.FromString(metricsPortName),
			Scheme: corev1.URISchemeHTTP,
		},
	}
}

// serverStartupProbe gives a slow start (plugin copy, MCP servers, hub
// database) up to five minutes before liveness takes over.
func serverStartupProbe() *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler:     serverHealthHandler(),
		PeriodSeconds:    5,
		TimeoutSeconds:   3,
		FailureThreshold: 60,
	}
}

// serverReadinessProbe takes a pod out of the Service after 30 seconds of
// failed checks.
func serverReadinessProbe() *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler:     serverHealthHandler(),
		PeriodSeconds:    10,
		TimeoutSeconds:   3,
		FailureThreshold: 3,
	}
}

// serverLivenessProbe restarts a server that stopped answering for two
// minutes; generous on purpose, a restart drops every open stream.
func serverLivenessProbe() *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler:     serverHealthHandler(),
		PeriodSeconds:    20,
		TimeoutSeconds:   5,
		FailureThreshold: 6,
	}
}

// envNameSafe matches a provider name usable as an environment variable
// suffix and a ConfigMap key.
var envNameSafe = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// fallbackChainEnv renders spec.fallback as the variables the server
// builds its chain from.
//
// The server builds the chain from CHATCLI_FALLBACK_PROVIDERS alone and
// installs it only when at least two of the listed providers resolve; it
// does not add its own --provider. So spec.provider goes first unless the
// list already names it (then the list order is the user's choice and is
// kept), with spec.model as its model. Without that, provider CLAUDEAI
// with fallback [OPENAI] would get no chain at all, and [OPENAI, XAI]
// would get a chain that never tries CLAUDEAI.
func fallbackChainEnv(instance *platformv1alpha1.Instance) map[string]string {
	env := map[string]string{}
	fb := instance.Spec.Fallback
	if fb == nil {
		return env
	}
	primary := strings.TrimSpace(instance.Spec.Provider)
	listed := false
	for _, p := range fb.Providers {
		if strings.EqualFold(p.Name, primary) {
			listed = true
			break
		}
	}

	names := make([]string, 0, len(fb.Providers)+1)
	if primary != "" && !listed {
		names = append(names, primary)
		if instance.Spec.Model != "" && envNameSafe.MatchString(primary) {
			env["CHATCLI_FALLBACK_MODEL_"+strings.ToUpper(primary)] = instance.Spec.Model
		}
	}
	for _, p := range fb.Providers {
		names = append(names, p.Name)
		if p.Model != "" {
			env["CHATCLI_FALLBACK_MODEL_"+p.Name] = p.Model
		}
	}
	env["CHATCLI_FALLBACK_PROVIDERS"] = strings.Join(names, ",")
	return env
}
