/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"fmt"

	pb "github.com/diillson/chatcli/proto/chatcli/v1"
)

// --- Interactive session (bidirectional stream) ---

// validateSessionMessage bounds every message of the interactive stream.
// A bidi stream is the one place where an unbounded client can keep
// sending: without this, a single connection can push arbitrary content
// and an unbounded metadata map into the server for as long as it stays
// open.
func validateSessionMessage(req interface{}) error {
	r, ok := req.(*pb.SessionMessage)
	if !ok {
		return nil
	}
	if err := validateBoundedString("content", r.Content, maxPromptBytes); err != nil {
		return err
	}
	if _, known := pb.SessionMessage_Type_name[int32(r.Type)]; !known {
		return fmt.Errorf("type is not a known SessionMessage type")
	}
	if len(r.Metadata) > maxSessionMetadataEntries {
		return fmt.Errorf("metadata carries more than %d entries", maxSessionMetadataEntries)
	}
	for k, v := range r.Metadata {
		if len(k) > maxSessionMetadataKeyLen {
			return fmt.Errorf("metadata key exceeds maximum length of %d characters", maxSessionMetadataKeyLen)
		}
		if len(v) > maxSessionMetadataValueBytes {
			return fmt.Errorf("metadata value for %q exceeds maximum size of %d bytes", k, maxSessionMetadataValueBytes)
		}
	}
	return nil
}

// --- AIOps ---

func validateGetAlerts(req interface{}) error {
	r, ok := req.(*pb.GetAlertsRequest)
	if !ok {
		return nil
	}
	if err := validateNamespace("namespace", r.Namespace); err != nil {
		return err
	}
	return validateResourceName("deployment", r.Deployment)
}

// validateAgenticStep bounds the remediation agent's turn. This is the
// widest request the server accepts — a full conversation history plus a
// fresh cluster snapshot — so every repeated and free-text field is
// bounded, not just the obvious ones.
func validateAgenticStep(req interface{}) error {
	r, ok := req.(*pb.AgenticStepRequest)
	if !ok {
		return nil
	}
	if err := validateBoundedString("issue_name", r.IssueName, maxIssueNameBytes); err != nil {
		return err
	}
	if err := validateNamespace("namespace", r.Namespace); err != nil {
		return err
	}
	if err := validateResourceKind("resource_kind", r.ResourceKind); err != nil {
		return err
	}
	if err := validateResourceName("resource_name", r.ResourceName); err != nil {
		return err
	}
	if err := validateLabel("signal_type", r.SignalType, maxSignalTypeLen, signalTypeRegex); err != nil {
		return err
	}
	if err := validateSeverity("severity", r.Severity); err != nil {
		return err
	}
	if err := validateBoundedString("description", r.Description, maxDescriptionBytes); err != nil {
		return err
	}
	if err := validateIntRange("risk_score", int(r.RiskScore), riskScoreMin, riskScoreMax, true); err != nil {
		return err
	}
	if err := validateBoundedString("kubernetes_context", r.KubernetesContext, maxK8sContextBytes); err != nil {
		return err
	}
	if err := validateBoundedString("insight_analysis", r.InsightAnalysis, maxInsightAnalysisBytes); err != nil {
		return err
	}
	if len(r.History) > maxAgenticHistoryEntries {
		return fmt.Errorf("history carries more than %d entries", maxAgenticHistoryEntries)
	}
	if len(r.InsightRecommendations) > maxInsightRecommendations {
		return fmt.Errorf("insight_recommendations carries more than %d entries", maxInsightRecommendations)
	}
	if len(r.InsightSuggestedActions) > maxInsightSuggestedActions {
		return fmt.Errorf("insight_suggested_actions carries more than %d entries", maxInsightSuggestedActions)
	}
	if err := validateIntRange("max_steps", int(r.MaxSteps), 1, maxAgenticSteps, true); err != nil {
		return err
	}
	return validateIntRange("current_step", int(r.CurrentStep), 1, maxAgenticSteps, true)
}

// --- Remote resource discovery ---

func validateAgentDefinition(req interface{}) error {
	r, ok := req.(*pb.GetAgentDefinitionRequest)
	if !ok {
		return nil
	}
	if r.Name == "" {
		return fmt.Errorf("name is required")
	}
	return validateResourceIdentifier("name", r.Name)
}

func validateSkillContent(req interface{}) error {
	r, ok := req.(*pb.GetSkillContentRequest)
	if !ok {
		return nil
	}
	if r.Name == "" {
		return fmt.Errorf("name is required")
	}
	return validateResourceIdentifier("name", r.Name)
}

// validateResourceIdentifier reuses the session-name rules for agent and
// skill names: both address a file on the server, so the same "no path
// separators, no traversal" guarantee applies.
func validateResourceIdentifier(field, name string) error {
	if len(name) > maxSessionNameLen {
		return fmt.Errorf("%s exceeds maximum length of %d characters", field, maxSessionNameLen)
	}
	if !sessionNameRegex.MatchString(name) {
		return fmt.Errorf("%s contains invalid characters (only alphanumeric, dash, underscore, dot allowed)", field)
	}
	return nil
}

// --- Conversation hub ---

func validateResolveActiveConversation(req interface{}) error {
	r, ok := req.(*pb.ResolveActiveConversationRequest)
	if !ok {
		return nil
	}
	return validateBoundedString("principal", r.Principal, maxPrincipalLen)
}

func validateNewConversation(req interface{}) error {
	r, ok := req.(*pb.NewConversationRequest)
	if !ok {
		return nil
	}
	return validateBoundedString("principal", r.Principal, maxPrincipalLen)
}

func validateAppendEvent(req interface{}) error {
	r, ok := req.(*pb.AppendEventRequest)
	if !ok {
		return nil
	}
	if r.ConvId == "" {
		return fmt.Errorf("conv_id is required")
	}
	if err := validateBoundedString("conv_id", r.ConvId, maxConvIDLen); err != nil {
		return err
	}
	if err := validateLabel("channel", r.Channel, maxChannelLen, channelRegex); err != nil {
		return err
	}
	if err := validateBoundedString("role", r.Role, maxEventRoleLen); err != nil {
		return err
	}
	if err := validateBoundedString("content", r.Content, maxEventContentBytes); err != nil {
		return err
	}
	return validateBoundedString("client_msg_id", r.ClientMsgId, maxClientMsgIDLen)
}

func validateReadConversation(req interface{}) error {
	r, ok := req.(*pb.ReadConversationRequest)
	if !ok {
		return nil
	}
	if r.ConvId == "" {
		return fmt.Errorf("conv_id is required")
	}
	if err := validateBoundedString("conv_id", r.ConvId, maxConvIDLen); err != nil {
		return err
	}
	if r.SinceSeq < 0 {
		return fmt.Errorf("since_seq must not be negative")
	}
	return validateIntRange("limit", int(r.Limit), 1, maxConversationReadLimit, true)
}

func validateSubscribeConversation(req interface{}) error {
	r, ok := req.(*pb.SubscribeConversationRequest)
	if !ok {
		return nil
	}
	if r.ConvId == "" {
		return fmt.Errorf("conv_id is required")
	}
	if err := validateBoundedString("conv_id", r.ConvId, maxConvIDLen); err != nil {
		return err
	}
	if r.SinceSeq < 0 {
		return fmt.Errorf("since_seq must not be negative")
	}
	return nil
}

func validateSetBinding(req interface{}) error {
	r, ok := req.(*pb.SetBindingRequest)
	if !ok {
		return nil
	}
	if r.Platform == "" {
		return fmt.Errorf("platform is required")
	}
	if err := validateLabel("platform", r.Platform, maxPlatformLen, channelRegex); err != nil {
		return err
	}
	if r.UserId == "" {
		return fmt.Errorf("user_id is required")
	}
	if err := validateBoundedString("user_id", r.UserId, maxChannelUserIDLen); err != nil {
		return err
	}
	return validateBoundedString("principal", r.Principal, maxPrincipalLen)
}

func validateListBindings(req interface{}) error {
	r, ok := req.(*pb.ListBindingsRequest)
	if !ok {
		return nil
	}
	return validateBoundedString("principal", r.Principal, maxPrincipalLen)
}
