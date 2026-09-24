/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package client

import (
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/diillson/chatcli/i18n"
)

// StopDetails is what Anthropic says about a refusal beside the stop
// reason (Messages API stop_details, populated only on stop_reason
// "refusal"): the policy category the classifier applied ("cyber", "bio",
// "reasoning_extraction", ... — informational, may be empty), an optional
// explanation, and the model the API recommends retrying on when it has
// one. The field names are the wire names, so both the buffered body and
// the message_delta event decode straight into it.
type StopDetails struct {
	Category         string `json:"category"`
	Explanation      string `json:"explanation"`
	RecommendedModel string `json:"recommended_model"`
}

// ParseAnthropicStopDetails reads stop_details out of a buffered Messages
// API body, or returns nil when the body carries none.
func ParseAnthropicStopDetails(body []byte) *StopDetails {
	var envelope struct {
		StopDetails *StopDetails `json:"stop_details"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.StopDetails == nil {
		return nil
	}
	if envelope.StopDetails.Category == "" && envelope.StopDetails.Explanation == "" && envelope.StopDetails.RecommendedModel == "" {
		return nil
	}
	return envelope.StopDetails
}

// Stop reasons a provider uses when it ends a reply without producing the
// text the caller asked for. Anthropic reports "refusal" when a streaming
// safety classifier stops the reply; OpenAI-compatible APIs report
// "content_filter" for the same thing.
const (
	StopReasonRefusal       = "refusal"
	StopReasonContentFilter = "content_filter"
	StopReasonMaxTokens     = "max_tokens"
)

// EmptyResponseError is a reply that came back with no text. It carries
// what the provider did say about it, the stop reason and the content
// blocks it did send, so the message names the cause instead of the
// generic "no response": a classifier refusal is not a network fault, and
// retrying the same request gives the same result.
type EmptyResponseError struct {
	Provider   string
	Model      string
	StopReason string
	Blocks     map[string]int // content block types seen, by count
	// Details is what the provider said about a refusal beyond the stop
	// reason, when it said anything (Anthropic stop_details); nil otherwise.
	Details *StopDetails
}

// Error renders the cause the provider gave.
func (e *EmptyResponseError) Error() string {
	provider := e.Provider
	if provider == "" {
		provider = "LLM"
	}
	switch {
	case e.Refused() && e.Category() != "":
		explanation := ""
		if e.Details.Explanation != "" {
			explanation = ": " + e.Details.Explanation
		}
		return i18n.T("llm.error.refusal_category", provider, e.StopReason, e.Category(), explanation)
	case e.Refused():
		return i18n.T("llm.error.refusal", provider, e.StopReason)
	case e.StopReason == StopReasonMaxTokens:
		return i18n.T("llm.error.empty_response_max_tokens", provider)
	case e.Blocks["thinking"] > 0:
		return i18n.T("llm.error.empty_reasoning_only", provider)
	case e.StopReason != "":
		return i18n.T("llm.error.empty_with_stop_reason", provider, e.StopReason, e.blockSummary())
	}
	return i18n.T("llm.error.no_response", provider)
}

// Refused reports whether a safety classifier stopped the reply.
func (e *EmptyResponseError) Refused() bool {
	return e != nil && (e.StopReason == StopReasonRefusal || e.StopReason == StopReasonContentFilter)
}

// Category is the policy category the provider named for the refusal, or
// "" when it named none. Nil-safe.
func (e *EmptyResponseError) Category() string {
	if e == nil || e.Details == nil {
		return ""
	}
	return e.Details.Category
}

// RecommendedModel is the model the provider recommends retrying on, or
// "" when it recommends none. Nil-safe.
func (e *EmptyResponseError) RecommendedModel() string {
	if e == nil || e.Details == nil {
		return ""
	}
	return e.Details.RecommendedModel
}

// blockSummary lists the blocks the provider did send, for the message.
func (e *EmptyResponseError) blockSummary() string {
	if len(e.Blocks) == 0 {
		return "none"
	}
	keys := make([]string, 0, len(e.Blocks))
	for k := range e.Blocks {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"×"+strconv.Itoa(e.Blocks[k]))
	}
	return strings.Join(parts, ", ")
}

// AsEmptyResponse unwraps err to the EmptyResponseError it carries, or nil.
func AsEmptyResponse(err error) *EmptyResponseError {
	var e *EmptyResponseError
	if errors.As(err, &e) {
		return e
	}
	return nil
}

// IsRefusal reports whether err is a reply a safety classifier stopped.
func IsRefusal(err error) bool {
	return AsEmptyResponse(err).Refused()
}
