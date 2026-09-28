/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"strings"
	"time"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// Annotations the REST API (POST /api/v1/incidents/:name/acknowledge and
// /snooze) writes on an Issue and the notification controller acts on.
const (
	// AnnotationIncidentAcknowledged set to "true" stops escalation of the
	// Issue: no further level is reached and no repeat is sent.
	AnnotationIncidentAcknowledged = "aiops.chatcli.io/acknowledged"
	// AnnotationIncidentAcknowledgedAt is the RFC 3339 acknowledgment time.
	AnnotationIncidentAcknowledgedAt = "aiops.chatcli.io/acknowledged-at"
	// AnnotationIncidentAcknowledgedBy identifies who acknowledged.
	AnnotationIncidentAcknowledgedBy = "aiops.chatcli.io/acknowledged-by"
	// AnnotationIncidentSnoozedUntil is an RFC 3339 time until which the
	// Issue sends no notification (except Resolved) and does not escalate.
	AnnotationIncidentSnoozedUntil = "aiops.chatcli.io/snoozed-until"
	// AnnotationIncidentSnoozedBy identifies who snoozed.
	AnnotationIncidentSnoozedBy = "aiops.chatcli.io/snoozed-by"
)

// IncidentAcknowledged reports whether someone acknowledged the Issue.
func IncidentAcknowledged(issue *platformv1alpha1.Issue) bool {
	return strings.EqualFold(strings.TrimSpace(issue.GetAnnotations()[AnnotationIncidentAcknowledged]), "true")
}

// IncidentSnoozedUntil returns the snooze end and whether the Issue is still
// snoozed at now. A missing or unparsable value means not snoozed.
func IncidentSnoozedUntil(issue *platformv1alpha1.Issue, now time.Time) (time.Time, bool) {
	end, ok := incidentSnoozeEnd(issue)
	if !ok || !end.After(now) {
		return time.Time{}, false
	}
	return end, true
}

// incidentSnoozeEnd parses the snooze annotation, past or future.
func incidentSnoozeEnd(issue *platformv1alpha1.Issue) (time.Time, bool) {
	raw := strings.TrimSpace(issue.GetAnnotations()[AnnotationIncidentSnoozedUntil])
	if raw == "" {
		return time.Time{}, false
	}
	end, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, false
	}
	return end, true
}
