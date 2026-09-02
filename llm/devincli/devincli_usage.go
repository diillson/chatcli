/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package devincli

import (
	"encoding/json"
	"strings"

	"github.com/diillson/chatcli/models"
)

// trajectoryFileName is the per-turn --export target inside the isolated
// workdir. The Devin CLI writes the conversation there in ATIF (Agent
// Trajectory Interchange Format) after the turn, and that document is the
// only place the CLI reports token usage: stdout carries just the reply.
const trajectoryFileName = "trajectory.json"

// atifTrajectory mirrors the subset of an ATIF document the usage reader
// needs. Two metric shapes coexist: the ATIF standard step.metrics
// (prompt_tokens / completion_tokens / cached_tokens / cost_usd) and the
// Devin-specific step.metadata.metrics (input_tokens / output_tokens /
// cache_creation_tokens / cache_read_tokens, with committed_acu_cost next
// to it). Numbers decode as float64 so an integer-valued float never fails
// the whole document.
type atifTrajectory struct {
	Agent struct {
		ModelName string `json:"model_name"`
	} `json:"agent"`
	Steps        []atifStep  `json:"steps"`
	FinalMetrics *atifTotals `json:"final_metrics"`
}

type atifStep struct {
	Source   string       `json:"source"`
	Metrics  *atifMetrics `json:"metrics"`
	Metadata struct {
		IsUserInput      bool         `json:"is_user_input"`
		GenerationModel  string       `json:"generation_model"`
		CommittedACUCost float64      `json:"committed_acu_cost"`
		Metrics          *devinMetric `json:"metrics"`
	} `json:"metadata"`
}

type atifMetrics struct {
	PromptTokens     float64 `json:"prompt_tokens"`
	CompletionTokens float64 `json:"completion_tokens"`
	CachedTokens     float64 `json:"cached_tokens"`
	CostUSD          float64 `json:"cost_usd"`
}

type devinMetric struct {
	InputTokens         float64 `json:"input_tokens"`
	OutputTokens        float64 `json:"output_tokens"`
	CacheCreationTokens float64 `json:"cache_creation_tokens"`
	CacheReadTokens     float64 `json:"cache_read_tokens"`
}

type atifTotals struct {
	TotalPromptTokens     float64 `json:"total_prompt_tokens"`
	TotalCompletionTokens float64 `json:"total_completion_tokens"`
	TotalCachedTokens     float64 `json:"total_cached_tokens"`
	TotalCostUSD          float64 `json:"total_cost_usd"`
}

// turnUsage is what one exported trajectory says about the turn.
type turnUsage struct {
	Usage   *models.UsageInfo
	ACUCost float64 // committed ACU cost summed over steps (informational)
	Model   string  // model the backend reports having generated with
}

// parseTrajectoryUsage sums the token metrics of every non-user step in an
// exported trajectory. Devin-native metrics win over the ATIF standard
// block on a step (they carry the cache split); the trajectory-level
// final_metrics is the fallback when no step carries metrics at all. A
// document with no usable numbers returns a nil Usage so the caller falls
// back to the character estimate exactly as before the export existed.
func parseTrajectoryUsage(raw []byte) (turnUsage, error) {
	var doc atifTrajectory
	if err := json.Unmarshal(raw, &doc); err != nil {
		return turnUsage{}, err
	}

	var out turnUsage
	usage := &models.UsageInfo{IsReal: true}
	for _, step := range doc.Steps {
		if step.Metadata.IsUserInput || strings.EqualFold(step.Source, "user") {
			continue
		}
		out.ACUCost += step.Metadata.CommittedACUCost
		if step.Metadata.GenerationModel != "" {
			out.Model = step.Metadata.GenerationModel
		}
		switch {
		case step.Metadata.Metrics != nil:
			m := step.Metadata.Metrics
			usage.PromptTokens += int(m.InputTokens)
			usage.CompletionTokens += int(m.OutputTokens)
			usage.CacheCreationInputTokens += int(m.CacheCreationTokens)
			usage.CacheReadInputTokens += int(m.CacheReadTokens)
			if step.Metrics != nil {
				usage.CostUSD += step.Metrics.CostUSD
			}
		case step.Metrics != nil:
			m := step.Metrics
			usage.PromptTokens += int(m.PromptTokens)
			usage.CompletionTokens += int(m.CompletionTokens)
			usage.CacheReadInputTokens += int(m.CachedTokens)
			usage.CostUSD += m.CostUSD
		}
	}
	if usage.PromptTokens == 0 && usage.CompletionTokens == 0 && doc.FinalMetrics != nil {
		usage.PromptTokens = int(doc.FinalMetrics.TotalPromptTokens)
		usage.CompletionTokens = int(doc.FinalMetrics.TotalCompletionTokens)
		usage.CacheReadInputTokens = int(doc.FinalMetrics.TotalCachedTokens)
		usage.CostUSD = doc.FinalMetrics.TotalCostUSD
	}
	if out.Model == "" {
		out.Model = doc.Agent.ModelName
	}
	if usage.PromptTokens == 0 && usage.CompletionTokens == 0 {
		return out, nil
	}
	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	out.Usage = usage
	return out, nil
}
