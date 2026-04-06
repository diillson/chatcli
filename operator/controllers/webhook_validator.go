/*
 * ChatCLI - Kubernetes Operator
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Security (L7): ValidatingWebhook for CRD business logic validation.
 * Prevents deletion of in-flight remediations and validates remediation plan specs.
 */
package controllers

import (
	"context"
	"fmt"
	"net/http"

	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// RemediationPlanValidator validates RemediationPlan CRD operations.
type RemediationPlanValidator struct {
	decoder admission.Decoder
}

// Handle implements the admission.Handler interface.
func (v *RemediationPlanValidator) Handle(ctx context.Context, req admission.Request) admission.Response {
	// Validate DELETE operations — prevent deletion of running remediations
	if req.Operation == "DELETE" {
		plan := &platformv1alpha1.RemediationPlan{}
		if err := v.decoder.Decode(req, plan); err != nil {
			// If we can't decode, allow (might be a direct API delete)
			return admission.Allowed("unable to decode, allowing")
		}

		if plan.Status.Phase == "Running" || plan.Status.Phase == "InProgress" {
			return admission.Denied(fmt.Sprintf(
				"cannot delete RemediationPlan %s/%s while in phase %q — wait for completion or cancel first",
				plan.Namespace, plan.Name, plan.Status.Phase,
			))
		}
	}

	// Validate CREATE/UPDATE — ensure action types are valid
	if req.Operation == "CREATE" || req.Operation == "UPDATE" {
		plan := &platformv1alpha1.RemediationPlan{}
		if err := v.decoder.Decode(req, plan); err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}

		// Validate each action in the plan
		for i, action := range plan.Spec.Actions {
			if action.Type == "" {
				return admission.Denied(fmt.Sprintf("action[%d] has empty type", i))
			}

			// Validate using resource allowlist for ApplyManifest actions
			if action.Type == platformv1alpha1.ActionApplyManifest {
				// The actual manifest content is validated at execution time
				// Here we just ensure the action structure is valid
				if action.Params == nil {
					return admission.Denied(fmt.Sprintf(
						"action[%d] (ApplyManifest) requires params with 'manifest' key", i))
				}
			}
		}
	}

	return admission.Allowed("validation passed")
}
