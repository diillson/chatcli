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
	// Extra carries what the standard block has no field for. Enterprise
	// builds of the CLI report ONLY this block, and for an Anthropic
	// backend they put the cache write there (a turn observed in the
	// field: prompt_tokens 14274 = cached_tokens 9098 + extra
	// cache_creation_input_tokens 5173 + 3 uncached). Without reading it
	// the write is priced as plain input instead of at the write rate.
	Extra struct {
		CacheCreationInputTokens float64 `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     float64 `json:"cache_read_input_tokens"`
	} `json:"extra"`
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
	// inputTotal is accumulated PER STEP, because one trajectory can mix
	// both metric shapes (and, across steps, backends): summing first and
	// classifying once would apply one schema to counters written under
	// another.
	inputTotal := 0
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
			stepModel := step.Metadata.GenerationModel
			if stepModel == "" {
				stepModel = doc.Agent.ModelName
			}
			inputTotal += int(m.InputTokens)
			if devinCacheAccounting(true, stepModel) == models.CacheAdditive {
				inputTotal += int(m.CacheCreationTokens) + int(m.CacheReadTokens)
			}
		case step.Metrics != nil:
			m := step.Metrics
			usage.PromptTokens += int(m.PromptTokens)
			usage.CompletionTokens += int(m.CompletionTokens)
			// cached_tokens is the read; a build that also spells it out
			// under extra reports the same number twice, so take the larger
			// rather than the sum.
			read := m.CachedTokens
			if m.Extra.CacheReadInputTokens > read {
				read = m.Extra.CacheReadInputTokens
			}
			usage.CacheReadInputTokens += int(read)
			usage.CacheCreationInputTokens += int(m.Extra.CacheCreationInputTokens)
			usage.CostUSD += m.CostUSD
			// ATIF standard block: cached_tokens (and the extra cache
			// write) are a subset of prompt_tokens, whatever model produced
			// the step.
			inputTotal += int(m.PromptTokens)
		}
	}
	if usage.PromptTokens == 0 && usage.CompletionTokens == 0 && doc.FinalMetrics != nil {
		usage.PromptTokens = int(doc.FinalMetrics.TotalPromptTokens)
		usage.CompletionTokens = int(doc.FinalMetrics.TotalCompletionTokens)
		usage.CacheReadInputTokens = int(doc.FinalMetrics.TotalCachedTokens)
		usage.CostUSD = doc.FinalMetrics.TotalCostUSD
		// final_metrics is the ATIF standard block's shape: subset.
		inputTotal = usage.PromptTokens
	}
	if out.Model == "" {
		out.Model = doc.Agent.ModelName
	}
	if usage.PromptTokens == 0 && usage.CompletionTokens == 0 {
		return out, nil
	}
	if inputTotal < usage.PromptTokens {
		inputTotal = usage.PromptTokens
	}
	usage.InputTokensTotal = inputTotal
	usage.TotalTokens = inputTotal + usage.CompletionTokens
	out.Usage = usage
	return out, nil
}

// devinCacheAccounting classifies ONE step's token counts.
//
// The Devin CLI fronts several backends and copies each one's counters into
// the same ATIF fields, so the field names alone do not settle the
// semantics — the model that generated does. An Anthropic backend reports
// cache reads and writes BESIDE input_tokens (they must be added to get the
// real input); the OpenAI/Kimi backends report the cached share INSIDE it
// (adding would double-count it). The trajectory names that model
// (step.metadata.generation_model, else agent.model_name), which is the
// right thing to classify on: the ChatCLI-facing alias may be an opaque
// account-level name that says nothing about the backend.
//
// The ATIF standard block is OpenAI-shaped by definition (prompt_tokens /
// cached_tokens), so it is always subset regardless of the model.
func devinCacheAccounting(nativeMetrics bool, model string) models.CacheAccounting {
	if !nativeMetrics {
		return models.CacheSubset
	}
	m := strings.ToLower(model)
	if strings.Contains(m, "claude") || strings.Contains(m, "fable") || strings.Contains(m, "anthropic") {
		return models.CacheAdditive
	}
	return models.CacheSubset
}
