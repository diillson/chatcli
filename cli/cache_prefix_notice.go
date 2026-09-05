/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Declaring the rewrites we make on purpose.
 *
 * The cache telemetry separates a rebuild ChatCLI asked for from a miss
 * caused by an unstable prefix, and alerts after three unexplained misses
 * in a row. It only knows what it is told, and three request fields that
 * arrived later change the prefix without telling it: the effort router
 * moves output_config between present and absent turn to turn, activating
 * a deferred tool changes the tools array that is serialized ahead of the
 * system block, and a task budget appearing mid-run adds a field that was
 * not there before. All three are our decisions, and all three used to
 * read as instability — so the alert fired at the user for something the
 * user did not do.
 *
 * A change is only reported when the shape actually differs from the last
 * request: announcing a rebuild every turn would tell the telemetry
 * nothing at all.
 */
package cli

import (
	"sort"
	"strconv"
	"strings"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
)

// wireShape is the part of a request outside the history that decides
// whether the provider can reuse the cached prefix.
type wireShape struct {
	effort      string
	toolNames   string
	taskBudgets bool
}

// String renders the shape for the debug log that explains a rebuild.
func (w wireShape) String() string {
	return "effort=" + w.effort + " budget=" + strconv.FormatBool(w.taskBudgets) + " tools=" + strconv.Itoa(strings.Count(w.toolNames, ",")+1)
}

// toolShape summarizes a tool array by the names it carries, in a stable
// order: a set that gained or lost a definition is a different prefix,
// while the same set serialized in a different order is not.
func toolShape(defs []models.ToolDefinition) string {
	if len(defs) == 0 {
		return ""
	}
	names := make([]string, 0, len(defs))
	for _, d := range defs {
		names = append(names, d.Function.Name)
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

// noteWireShape compares this turn's request shape against the last one
// and, when they differ, tells the telemetry the coming cache write is a
// rebuild we asked for. The first turn of a session establishes the shape
// without reporting anything: there is no cache to rebuild yet.
func (cli *ChatCLI) noteWireShape(effort client.SkillEffort, defs []models.ToolDefinition, hasTaskBudget bool) {
	if cli == nil {
		return
	}
	shape := wireShape{
		effort:      string(effort),
		toolNames:   toolShape(defs),
		taskBudgets: hasTaskBudget,
	}
	prev, established := cli.lastWireShape, cli.wireShapeSet
	cli.lastWireShape, cli.wireShapeSet = shape, true
	if !established || prev == shape {
		return
	}
	cli.notePrefixChanged("wire shape: " + prev.String() + " → " + shape.String())
}
