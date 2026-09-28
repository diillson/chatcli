/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"context"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// TLSConditionType is the Instance condition that reports whether the TLS
// settings can start a server. Present only while spec.server.tls.enabled
// is true.
//
// tls.enabled with no tls.secretName renders --tls-cert and --tls-key
// pointing at files nothing mounts; the server exits on the certificate
// load and the pod crash-loops with the reason buried in its logs. The
// operator says it on the Instance instead and does not provision the
// Deployment, the same way AuthenticationConfigured does.
const TLSConditionType = "TLSConfigured"

// tlsSecretMissingMessage names the fix and the opt-out.
const tlsSecretMissingMessage = "spec.server.tls.enabled is true but spec.server.tls.secretName is empty, so the server has no certificate to load and would exit on startup: " +
	"set spec.server.tls.secretName to a Secret holding tls.crt and tls.key, or set spec.server.tls.enabled to false"

// applyTLSCondition sets the TLSConfigured condition on the in-memory
// Instance and reports whether provisioning must stop. With TLS disabled
// the condition is removed, so it never lingers after the fix was to turn
// TLS off.
func applyTLSCondition(ctx context.Context, instance *platformv1alpha1.Instance) bool {
	tls := instance.Spec.Server.TLS
	if tls == nil || !tls.Enabled {
		meta.RemoveStatusCondition(&instance.Status.Conditions, TLSConditionType)
		return false
	}
	cond := metav1.Condition{
		Type:               TLSConditionType,
		Status:             metav1.ConditionTrue,
		Reason:             "SecretConfigured",
		Message:            "server certificate from Secret " + tls.SecretName,
		ObservedGeneration: instance.Generation,
	}
	blocked := tls.SecretName == ""
	if blocked {
		cond.Status = metav1.ConditionFalse
		cond.Reason = "SecretNameMissing"
		cond.Message = tlsSecretMissingMessage
		instance.Status.Ready = false
		log.FromContext(ctx).Info("instance not provisioned: " + tlsSecretMissingMessage)
	}
	meta.SetStatusCondition(&instance.Status.Conditions, cond)
	return blocked
}
