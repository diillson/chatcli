/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package workers

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/diillson/chatcli/cli/ctxmgr"
	"github.com/diillson/chatcli/models"
)

// Deferred tool definitions.
//
// Every request carries the full JSON schema of every tool the run can
// call. For the built-in set that is a bounded cost, but MCP servers are
// unbounded: a handful of servers puts tens of thousands of tokens of
// schema in front of the model on every turn, cached at best, and the
// compaction budget can only reserve room for it — nothing shrinks it.
//
// So the schemas stop traveling by default once they are large enough to
// matter. What travels instead is a one-line index — name and summary,
// small enough to live in the cached prefix — plus one tool the model
// calls to pull the full schema of whatever it actually needs. Tools it
// asks for stay loaded for the rest of the run.
//
// The mechanism is deliberately client-side. Anthropic serves a
// server-side equivalent for its own models; doing the selection here
// means every provider gets it, including the ones that will never have
// such an endpoint.

// FindToolsName is the tool the model calls to load deferred schemas.
const FindToolsName = "find_tools"

// deferFloorChars is the payload below which nothing is deferred. A small
// tool set costs less than the round trip a search would add, so the
// behavior of a plain install is unchanged.
const deferFloorChars = 8000

// DeferThresholdChars is the payload above which deferrable schemas stop
// traveling, derived from the model's context window so a large window
// tolerates a larger tool set. Never below the floor.
func DeferThresholdChars(windowTokens, charsPerToken int) int {
	if windowTokens <= 0 || charsPerToken <= 0 {
		return deferFloorChars
	}
	// A twentieth of the window: past that, tool schemas are competing
	// with the conversation rather than describing it.
	limit := windowTokens * charsPerToken / 20
	if limit < deferFloorChars {
		return deferFloorChars
	}
	return limit
}

// ToolPlan is what one turn ships and what it advertises instead.
type ToolPlan struct {
	// Defs are the definitions that travel on the wire this turn.
	Defs []models.ToolDefinition
	// Index is the one-line-per-tool catalog of everything deferred,
	// for the cached prefix. Empty when nothing was deferred.
	Index string
	// Deferred counts the schemas left out of the request.
	Deferred int
}

// PlanToolDefs decides what a turn ships.
//
// core always travels — the loop cannot work without it. deferrable
// travels too while the whole payload stays under the threshold; past it
// only the tools the model already asked for (activated) travel, next to
// the search tool, and the rest are advertised through the index.
func PlanToolDefs(core, deferrable []models.ToolDefinition, activated map[string]bool, threshold int) ToolPlan {
	all := append(append([]models.ToolDefinition{}, core...), deferrable...)
	if len(deferrable) == 0 || serializedChars(all) <= threshold {
		return ToolPlan{Defs: all}
	}

	plan := ToolPlan{Defs: append([]models.ToolDefinition{}, core...)}
	indexed := make([]models.ToolDefinition, 0, len(deferrable))
	for _, d := range deferrable {
		if activated[d.Function.Name] {
			plan.Defs = append(plan.Defs, d)
			continue
		}
		indexed = append(indexed, d)
	}
	if len(indexed) == 0 {
		return plan
	}
	plan.Defs = append(plan.Defs, FindToolsDefinition())
	plan.Index = ToolIndex(indexed)
	plan.Deferred = len(indexed)
	return plan
}

// FindToolsDefinition is the definition of the search tool itself. It is
// never deferred — a model that cannot ask for tools cannot recover any.
func FindToolsDefinition() models.ToolDefinition {
	return models.ToolDefinition{
		Type: "function",
		Function: models.ToolFunctionDef{
			Name: FindToolsName,
			Description: "Load the full definition of tools that are listed in the available-tools index but not yet loaded. " +
				"Call this before using any tool from that index; the tools it returns stay available for the rest of the session.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "What the tool should do, or its exact name from the index.",
					},
				},
				"required": []string{"query"},
			},
		},
	}
}

// ToolIndex renders the deferred set as one line per tool: the name the
// model must ask for, and enough of the description to choose by.
func ToolIndex(defs []models.ToolDefinition) string {
	if len(defs) == 0 {
		return ""
	}
	lines := make([]string, 0, len(defs))
	for _, d := range defs {
		lines = append(lines, "- "+d.Function.Name+": "+firstSentence(d.Function.Description))
	}
	sort.Strings(lines)
	return "AVAILABLE TOOLS (definitions not loaded — call " + FindToolsName +
		" with what you need before using one):\n" + strings.Join(lines, "\n")
}

// SearchToolDefs ranks the deferred set against a query. An exact name
// match wins outright, because a model quoting a name from the index is
// asking for that tool and not for something like it.
func SearchToolDefs(defs []models.ToolDefinition, query string, k int) []models.ToolDefinition {
	q := strings.TrimSpace(query)
	if q == "" || len(defs) == 0 {
		return nil
	}
	for _, d := range defs {
		if strings.EqualFold(d.Function.Name, q) {
			return []models.ToolDefinition{d}
		}
	}
	docs := make([]string, 0, len(defs))
	for _, d := range defs {
		docs = append(docs, d.Function.Name+" "+d.Function.Description)
	}
	hits := ctxmgr.RankDocsBM25(docs, q, k)
	out := make([]models.ToolDefinition, 0, len(hits))
	for _, h := range hits {
		if h.Index >= 0 && h.Index < len(defs) {
			out = append(out, defs[h.Index])
		}
	}
	return out
}

// firstSentence keeps an index line short without cutting mid-word.
func firstSentence(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if i := strings.Index(s, ". "); i > 0 && i < 200 {
		return s[:i+1]
	}
	if len(s) <= 200 {
		return s
	}
	cut := s[:200]
	if i := strings.LastIndex(cut, " "); i > 0 {
		cut = cut[:i]
	}
	return cut + "…"
}

func serializedChars(defs []models.ToolDefinition) int {
	if len(defs) == 0 {
		return 0
	}
	raw, err := json.Marshal(defs)
	if err != nil {
		return 0
	}
	return len(raw)
}
