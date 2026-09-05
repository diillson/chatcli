/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"fmt"
	"regexp"
	"strings"
)

// The validator registry is the single place that decides how each RPC's
// request is validated. It is a map rather than a switch on purpose: a
// test walks the generated service descriptor and fails when a method has
// no entry, so a new RPC cannot ship without an explicit decision — either
// a real validator or validateNoFields, which states "nothing to check"
// out loud instead of leaving a silent gap.
var requestValidators = map[string]func(interface{}) error{
	// Prompting
	"SendPrompt":         validateSendPrompt,
	"StreamPrompt":       validateStreamPrompt,
	"InteractiveSession": validateSessionMessage,

	// Sessions
	"ListSessions":  validateNoFields,
	"LoadSession":   validateSessionName,
	"SaveSession":   validateSaveSession,
	"DeleteSession": validateSessionName,

	// Server metadata and probes
	"GetServerInfo":    validateNoFields,
	"GetWatcherStatus": validateNoFields,
	"Health":           validateNoFields,

	// AIOps
	"GetAlerts":    validateGetAlerts,
	"AnalyzeIssue": validateAnalyzeIssue,
	"AgenticStep":  validateAgenticStep,

	// Remote resource discovery
	"ListRemotePlugins":   validateNoFields,
	"ListRemoteAgents":    validateNoFields,
	"ListRemoteSkills":    validateNoFields,
	"GetAgentDefinition":  validateAgentDefinition,
	"GetSkillContent":     validateSkillContent,
	"ExecuteRemotePlugin": validateExecutePlugin,
	"DownloadPlugin":      validateDownloadPlugin,

	// Conversation hub
	"ResolveActiveConversation": validateResolveActiveConversation,
	"NewConversation":           validateNewConversation,
	"AppendEvent":               validateAppendEvent,
	"ReadConversation":          validateReadConversation,
	"SubscribeConversation":     validateSubscribeConversation,
	"SetBinding":                validateSetBinding,
	"ListBindings":              validateListBindings,
}

// validateNoFields is the explicit "this request carries nothing worth
// bounding" decision. Named rather than nil so the registry test can tell
// a considered no-op from a forgotten method.
func validateNoFields(interface{}) error { return nil }

// --- Kubernetes naming rules (RFC 1123) ---

var (
	// rfc1123LabelRegex matches a DNS-1123 label: namespaces, and every
	// dot-separated component of a resource name.
	rfc1123LabelRegex = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	// rfc1123SubdomainRegex matches a DNS-1123 subdomain: most Kubernetes
	// object names.
	rfc1123SubdomainRegex = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
	// k8sKindRegex matches a Kubernetes kind: UpperCamelCase, letters and
	// digits only.
	k8sKindRegex = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*$`)
	// signalTypeRegex bounds the AIOps signal label to a safe charset.
	signalTypeRegex = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	// channelRegex bounds a hub channel/platform label.
	channelRegex = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
)

// validateNamespace enforces the DNS-1123 label rules Kubernetes itself
// applies to a namespace. Empty is accepted: every RPC that carries a
// namespace treats it as an optional filter.
func validateNamespace(field, ns string) error {
	if ns == "" {
		return nil
	}
	if len(ns) > maxNamespaceLen {
		return fmt.Errorf("%s exceeds maximum length of %d characters", field, maxNamespaceLen)
	}
	if !rfc1123LabelRegex.MatchString(ns) {
		return fmt.Errorf("%s must be a valid Kubernetes namespace (RFC 1123 label: lowercase alphanumeric or '-')", field)
	}
	return nil
}

// validateResourceName enforces the DNS-1123 subdomain rules Kubernetes
// applies to most object names. Empty is accepted.
func validateResourceName(field, name string) error {
	if name == "" {
		return nil
	}
	if len(name) > maxResourceNameLen {
		return fmt.Errorf("%s exceeds maximum length of %d characters", field, maxResourceNameLen)
	}
	if !rfc1123SubdomainRegex.MatchString(name) {
		return fmt.Errorf("%s must be a valid Kubernetes object name (RFC 1123 subdomain)", field)
	}
	return nil
}

// validateResourceKind bounds a Kubernetes kind. Empty is accepted.
func validateResourceKind(field, kind string) error {
	if kind == "" {
		return nil
	}
	if len(kind) > maxKindLen {
		return fmt.Errorf("%s exceeds maximum length of %d characters", field, maxKindLen)
	}
	if !k8sKindRegex.MatchString(kind) {
		return fmt.Errorf("%s must be a Kubernetes kind (letters and digits only)", field)
	}
	return nil
}

// allowedSeverities is the union of the Issue CRD enum (critical/high/
// medium/low) and the watcher's own labels (info/warning), so a caller on
// either path validates. Matching is case-insensitive.
var allowedSeverities = map[string]bool{
	"critical": true, "high": true, "medium": true, "low": true,
	"info": true, "warning": true,
}

// validateSeverity checks the severity enum. Empty is accepted: severity
// is optional on every request that carries it.
func validateSeverity(field, severity string) error {
	if severity == "" {
		return nil
	}
	if len(severity) > maxSeverityLen {
		return fmt.Errorf("%s exceeds maximum length of %d characters", field, maxSeverityLen)
	}
	if !allowedSeverities[strings.ToLower(severity)] {
		return fmt.Errorf("%s must be one of: critical, high, medium, low, warning, info", field)
	}
	return nil
}

// validateBoundedString enforces a maximum byte length on a free-text field.
func validateBoundedString(field, value string, limit int) error {
	if len(value) > limit {
		return fmt.Errorf("%s exceeds maximum size of %d bytes", field, limit)
	}
	return nil
}

// validateLabel enforces a bounded, safe charset on an identifier-shaped
// field (channel, platform, signal type). Empty is accepted.
func validateLabel(field, value string, limit int, re *regexp.Regexp) error {
	if value == "" {
		return nil
	}
	if len(value) > limit {
		return fmt.Errorf("%s exceeds maximum length of %d characters", field, limit)
	}
	if !re.MatchString(value) {
		return fmt.Errorf("%s contains invalid characters (alphanumeric, '-', '_', '.' allowed)", field)
	}
	return nil
}

// validateIntRange bounds a numeric field. Zero is accepted as "unset" for
// every field that uses it, so the caller passes zeroAllowed explicitly.
func validateIntRange(field string, value, low, high int, zeroAllowed bool) error {
	if value == 0 && zeroAllowed {
		return nil
	}
	if value < low || value > high {
		return fmt.Errorf("%s must be between %d and %d", field, low, high)
	}
	return nil
}
