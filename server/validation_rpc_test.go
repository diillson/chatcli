/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"strings"
	"testing"

	pb "github.com/diillson/chatcli/proto/chatcli/v1"
)

func longString(n int) string { return strings.Repeat("x", n) }

// Each RPC's validator, exercised through the same dispatch the
// interceptors use, so the registry wiring is covered along with the rules.
func TestValidators_RejectAndAccept(t *testing.T) {
	const svc = "/chatcli.v1.ChatCLIService/"

	cases := []struct {
		name    string
		method  string
		req     interface{}
		wantErr bool
	}{
		// Prompting
		{"SendPrompt ok", "SendPrompt", &pb.SendPromptRequest{Prompt: "hi", MaxTokens: 100}, false},
		{"SendPrompt oversized", "SendPrompt", &pb.SendPromptRequest{Prompt: longString(maxPromptBytes + 1)}, true},
		{"SendPrompt max_tokens too high", "SendPrompt", &pb.SendPromptRequest{Prompt: "hi", MaxTokens: maxTokensMax + 1}, true},
		{"StreamPrompt ok", "StreamPrompt", &pb.StreamPromptRequest{Prompt: "hi"}, false},

		// Sessions
		{"SaveSession ok", "SaveSession", &pb.SaveSessionRequest{Name: "my-session.1"}, false},
		{"SaveSession empty", "SaveSession", &pb.SaveSessionRequest{Name: ""}, true},
		{"SaveSession traversal", "SaveSession", &pb.SaveSessionRequest{Name: "../../etc/passwd"}, true},
		{"LoadSession ok", "LoadSession", &pb.LoadSessionRequest{Name: "s1"}, false},
		{"DeleteSession bad chars", "DeleteSession", &pb.DeleteSessionRequest{Name: "a b"}, true},
		{"ListSessions", "ListSessions", &pb.ListSessionsRequest{}, false},
		{"Health", "Health", &pb.HealthRequest{}, false},

		// AIOps
		{"GetAlerts ok", "GetAlerts", &pb.GetAlertsRequest{Namespace: "prod", Deployment: "api"}, false},
		{"GetAlerts bad ns", "GetAlerts", &pb.GetAlertsRequest{Namespace: "Prod"}, true},
		{"AnalyzeIssue ok", "AnalyzeIssue", &pb.AnalyzeIssueRequest{IssueName: "inc-1"}, false},
		{"AnalyzeIssue oversized ctx", "AnalyzeIssue", &pb.AnalyzeIssueRequest{KubernetesContext: longString(maxK8sContextBytes + 1)}, true},
		{"AgenticStep ok", "AgenticStep", &pb.AgenticStepRequest{
			IssueName: "inc-1", Namespace: "prod", ResourceKind: "Deployment",
			ResourceName: "api", SignalType: "oom_kill", Severity: "high",
			RiskScore: 40, MaxSteps: 5, CurrentStep: 1,
		}, false},
		{"AgenticStep bad kind", "AgenticStep", &pb.AgenticStepRequest{ResourceKind: "Deploy-ment"}, true},
		{"AgenticStep bad severity", "AgenticStep", &pb.AgenticStepRequest{Severity: "sev1"}, true},
		{"AgenticStep bad signal", "AgenticStep", &pb.AgenticStepRequest{SignalType: "oom kill"}, true},
		{"AgenticStep risk out of range", "AgenticStep", &pb.AgenticStepRequest{RiskScore: 500}, true},
		{"AgenticStep step out of range", "AgenticStep", &pb.AgenticStepRequest{CurrentStep: maxAgenticSteps + 1}, true},
		{"AgenticStep oversized issue", "AgenticStep", &pb.AgenticStepRequest{IssueName: longString(maxIssueNameBytes + 1)}, true},
		{"AgenticStep oversized insight", "AgenticStep", &pb.AgenticStepRequest{InsightAnalysis: longString(maxInsightAnalysisBytes + 1)}, true},
		{"AgenticStep oversized description", "AgenticStep", &pb.AgenticStepRequest{Description: longString(maxDescriptionBytes + 1)}, true},
		{"AgenticStep oversized k8s ctx", "AgenticStep", &pb.AgenticStepRequest{KubernetesContext: longString(maxK8sContextBytes + 1)}, true},

		// Remote discovery
		{"ListRemotePlugins", "ListRemotePlugins", &pb.ListRemotePluginsRequest{}, false},
		{"ListRemoteAgents", "ListRemoteAgents", &pb.ListRemoteAgentsRequest{}, false},
		{"ListRemoteSkills", "ListRemoteSkills", &pb.ListRemoteSkillsRequest{}, false},
		{"GetAgentDefinition ok", "GetAgentDefinition", &pb.GetAgentDefinitionRequest{Name: "reviewer"}, false},
		{"GetAgentDefinition empty", "GetAgentDefinition", &pb.GetAgentDefinitionRequest{}, true},
		{"GetAgentDefinition traversal", "GetAgentDefinition", &pb.GetAgentDefinitionRequest{Name: "../../secrets"}, true},
		{"GetSkillContent ok", "GetSkillContent", &pb.GetSkillContentRequest{Name: "deploy"}, false},
		{"GetSkillContent empty", "GetSkillContent", &pb.GetSkillContentRequest{}, true},
		{"ExecuteRemotePlugin ok", "ExecuteRemotePlugin", &pb.ExecuteRemotePluginRequest{PluginName: "@git"}, false},
		{"ExecuteRemotePlugin empty", "ExecuteRemotePlugin", &pb.ExecuteRemotePluginRequest{}, true},
		{"DownloadPlugin ok", "DownloadPlugin", &pb.DownloadPluginRequest{PluginName: "@git"}, false},
		{"DownloadPlugin empty", "DownloadPlugin", &pb.DownloadPluginRequest{}, true},

		// Conversation hub
		{"ResolveActiveConversation ok", "ResolveActiveConversation", &pb.ResolveActiveConversationRequest{Principal: "alice"}, false},
		{"ResolveActiveConversation oversized", "ResolveActiveConversation", &pb.ResolveActiveConversationRequest{Principal: longString(maxPrincipalLen + 1)}, true},
		{"NewConversation ok", "NewConversation", &pb.NewConversationRequest{}, false},
		{"NewConversation oversized", "NewConversation", &pb.NewConversationRequest{Principal: longString(maxPrincipalLen + 1)}, true},
		{"AppendEvent ok", "AppendEvent", &pb.AppendEventRequest{ConvId: "c1", Channel: "slack", Role: "user", Content: "hi"}, false},
		{"AppendEvent no conv", "AppendEvent", &pb.AppendEventRequest{Channel: "slack"}, true},
		{"AppendEvent bad channel", "AppendEvent", &pb.AppendEventRequest{ConvId: "c1", Channel: "sl ack"}, true},
		{"AppendEvent oversized content", "AppendEvent", &pb.AppendEventRequest{ConvId: "c1", Content: longString(maxEventContentBytes + 1)}, true},
		{"AppendEvent oversized msg id", "AppendEvent", &pb.AppendEventRequest{ConvId: "c1", ClientMsgId: longString(maxClientMsgIDLen + 1)}, true},
		{"ReadConversation ok", "ReadConversation", &pb.ReadConversationRequest{ConvId: "c1", Limit: 50}, false},
		{"ReadConversation no conv", "ReadConversation", &pb.ReadConversationRequest{}, true},
		{"ReadConversation negative seq", "ReadConversation", &pb.ReadConversationRequest{ConvId: "c1", SinceSeq: -1}, true},
		{"ReadConversation limit too high", "ReadConversation", &pb.ReadConversationRequest{ConvId: "c1", Limit: maxConversationReadLimit + 1}, true},
		{"SubscribeConversation ok", "SubscribeConversation", &pb.SubscribeConversationRequest{ConvId: "c1"}, false},
		{"SubscribeConversation no conv", "SubscribeConversation", &pb.SubscribeConversationRequest{}, true},
		{"SubscribeConversation negative seq", "SubscribeConversation", &pb.SubscribeConversationRequest{ConvId: "c1", SinceSeq: -5}, true},
		{"SetBinding ok", "SetBinding", &pb.SetBindingRequest{Platform: "slack", UserId: "U1", Principal: "alice"}, false},
		{"SetBinding no platform", "SetBinding", &pb.SetBindingRequest{UserId: "U1"}, true},
		{"SetBinding no user", "SetBinding", &pb.SetBindingRequest{Platform: "slack"}, true},
		{"SetBinding bad platform", "SetBinding", &pb.SetBindingRequest{Platform: "sl/ack", UserId: "U1"}, true},
		{"ListBindings ok", "ListBindings", &pb.ListBindingsRequest{}, false},
		{"ListBindings oversized", "ListBindings", &pb.ListBindingsRequest{Principal: longString(maxPrincipalLen + 1)}, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateRequest(svc+c.method, c.req)
			if c.wantErr && err == nil {
				t.Errorf("expected rejection, got nil")
			}
			if !c.wantErr && err != nil {
				t.Errorf("expected acceptance, got %v", err)
			}
		})
	}
}

// A validator must never panic or reject on a message it was not written
// for: the registry dispatches by method name, and a mismatched payload is
// a bug elsewhere, not something to turn into a validation error.
func TestValidators_IgnoreMismatchedPayloads(t *testing.T) {
	const svc = "/chatcli.v1.ChatCLIService/"
	for method := range requestValidators {
		if err := validateRequest(svc+method, &pb.HealthRequest{}); err != nil {
			t.Errorf("%s rejected a mismatched payload: %v", method, err)
		}
	}
}

func TestValidators_UnknownMethodIsNotRejected(t *testing.T) {
	if err := validateRequest("/chatcli.v1.ChatCLIService/SomethingNew", &pb.HealthRequest{}); err != nil {
		t.Errorf("an unregistered method must not be rejected here: %v", err)
	}
}

func TestValidateResourceKindAndNamespaceEdges(t *testing.T) {
	if err := validateResourceKind("kind", longString(maxKindLen+1)); err == nil {
		t.Error("oversized kind accepted")
	}
	if err := validateNamespace("ns", ""); err != nil {
		t.Errorf("empty namespace must be accepted: %v", err)
	}
	if err := validateResourceName("name", ""); err != nil {
		t.Errorf("empty resource name must be accepted: %v", err)
	}
	if err := validateResourceKind("kind", ""); err != nil {
		t.Errorf("empty kind must be accepted: %v", err)
	}
	if err := validateLabel("l", longString(20), 10, signalTypeRegex); err == nil {
		t.Error("oversized label accepted")
	}
	if err := validateSeverity("severity", longString(maxSeverityLen+1)); err == nil {
		t.Error("oversized severity accepted")
	}
	if err := validateIntRange("n", 0, 1, 10, false); err == nil {
		t.Error("zero rejected only when zeroAllowed is false")
	}
	if err := validateIntRange("n", 0, 1, 10, true); err != nil {
		t.Errorf("zero must pass when allowed: %v", err)
	}
}
