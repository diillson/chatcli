/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package controllers

import (
	"context"
	"net"
	"strings"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Refusing to provision an unauthenticated instance on a reachable bind.
//
// The server itself refuses to start in that shape, because a listener on
// every interface with no credential admits any caller as an
// administrator. Inside a cluster that is every pod. Left to the pod, the
// refusal shows up as a crash loop and the reason sits in container logs;
// surfacing it on the Instance instead means the operator says what is
// wrong where the user is already looking.
//
// The check mirrors the server's exactly: loopback may run open, anything
// else needs a credential, and an address that does not parse counts as
// reachable — the safe answer to "unknown" is to require one.

// AuthConditionType is the Instance condition raised when the spec would
// provision a server that refuses to start for want of a credential.
const AuthConditionType = "AuthenticationConfigured"

// instanceBindAddress is the address the pod will listen on: the spec's
// own value when set, otherwise the in-cluster default the server picks
// for itself, which is every interface.
func instanceBindAddress(instance *platformv1alpha1.Instance) string {
	if instance != nil && instance.Spec.Server.Security != nil {
		if addr := strings.TrimSpace(instance.Spec.Server.Security.BindAddress); addr != "" {
			return addr
		}
	}
	// No bindAddress in the spec: the container runs with
	// KUBERNETES_SERVICE_HOST set, so the server binds every interface.
	return "0.0.0.0"
}

// instanceHasCredential reports whether the spec provisions either
// authentication mechanism — a shared token, a JWT secret, or one of the
// two supplied directly through extraEnv.
func instanceHasCredential(instance *platformv1alpha1.Instance) bool {
	if instance == nil {
		return false
	}
	if instance.Spec.Server.Token != nil {
		return true
	}
	if sec := instance.Spec.Server.Security; sec != nil && sec.JWTSecretRef != nil {
		return true
	}
	for _, env := range instance.Spec.ExtraEnv {
		switch env.Name {
		case "CHATCLI_SERVER_TOKEN", "CHATCLI_JWT_SECRET":
			if env.Value != "" || env.ValueFrom != nil {
				return true
			}
		}
	}
	return false
}

// instanceAuthUnconfigured reports whether this Instance would provision a
// server that refuses to start: reachable bind, no credential.
func instanceAuthUnconfigured(instance *platformv1alpha1.Instance) bool {
	if instanceHasCredential(instance) {
		return false
	}
	return !isLoopbackAddress(instanceBindAddress(instance))
}

// authUnconfiguredMessage is what the condition and the event say. It
// names both ways to fix it and the one way to opt out.
const authUnconfiguredMessage = "server would listen on a reachable address with no credential, and refuses to start in that shape: " +
	"set spec.server.token, or spec.server.security.jwtSecretRef, or bind loopback with spec.server.security.bindAddress=127.0.0.1"

// isLoopbackAddress mirrors the server's own check.
func isLoopbackAddress(addr string) bool {
	host := strings.TrimSpace(addr)
	if host == "" {
		return false
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// recordAuthCondition writes the AuthenticationConfigured condition for
// this Instance and reports whether provisioning must stop.
//
// Stopping is not an error: there is nothing to retry until the spec
// changes, and a requeue would only rewrite the same condition. The
// condition is the output — it is what the user reads.
func (r *InstanceReconciler) recordAuthCondition(ctx context.Context, instance *platformv1alpha1.Instance) bool {
	log := log.FromContext(ctx)
	blocked := instanceAuthUnconfigured(instance)

	cond := metav1.Condition{
		Type:               AuthConditionType,
		Status:             metav1.ConditionTrue,
		Reason:             "CredentialConfigured",
		Message:            "server has a credential, or binds loopback only",
		ObservedGeneration: instance.Generation,
	}
	if blocked {
		cond.Status = metav1.ConditionFalse
		cond.Reason = "CredentialMissing"
		cond.Message = authUnconfiguredMessage
		instance.Status.Ready = false
	}
	meta.SetStatusCondition(&instance.Status.Conditions, cond)

	if !blocked {
		return false
	}
	if err := r.Status().Update(ctx, instance); err != nil {
		log.Error(err, "failed to record the missing-credential condition")
	}
	log.Info("instance not provisioned: " + authUnconfiguredMessage)
	return true
}
