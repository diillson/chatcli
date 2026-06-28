/*
 * ChatCLI - Adapter feeding the @graphview tool from live CLI state.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Implements plugins.GraphSourceProvider so @graphview's "knowledge" and
 * "conversation" sources render real data:
 *   - KnowledgeGraph: the in-core knowledge graph (the same substrate /graph
 *     draws statically), converted to the renderable node/edge shape.
 *   - ConversationGraph: a graph of the whole session — the turn thread and the
 *     tools it invoked, PLUS everything attached with /context (context bases
 *     with their files, knowledge corpora) — all hanging off a session root.
 *
 * Wired via plugins.SetGraphSourceProvider at startup. The "json" source needs
 * no provider, so @graphview works even before this is bound.
 */
package cli

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/diillson/chatcli/cli/plugins"
)

// graphViewMaxConversationMsgs caps how many recent messages the conversation
// graph includes, so a long session stays a legible graph, not a hairball.
const graphViewMaxConversationMsgs = 120

// graphViewMaxFilesPerContext caps how many files of an attached context become
// their own nodes; the remainder is summarized in a single "+N more" node.
const graphViewMaxFilesPerContext = 15

// graphViewPluginAdapter is the concrete plugins.GraphSourceProvider.
type graphViewPluginAdapter struct {
	cli *ChatCLI
}

// KnowledgeGraph converts the in-core knowledge graph to renderable data.
func (a *graphViewPluginAdapter) KnowledgeGraph() (plugins.GraphData, error) {
	g := a.cli.buildKnowledgeGraph()
	data := plugins.GraphData{
		Nodes: make([]plugins.GraphNode, 0, g.Len()),
		Edges: make([]plugins.GraphEdge, 0, g.Edges()),
	}
	for _, n := range g.Nodes() {
		label := strings.TrimSpace(n.Title)
		if label == "" {
			label = n.ID
		}
		data.Nodes = append(data.Nodes, plugins.GraphNode{
			ID:      n.ID,
			Label:   label,
			Kind:    string(n.Kind),
			Summary: n.Summary,
			Weight:  n.Weight,
		})
	}
	seen := make(map[string]bool)
	for _, n := range g.Nodes() {
		for _, nb := range g.Neighbors(n.ID) {
			x, y := n.ID, nb.ID
			if x > y {
				x, y = y, x
			}
			key := x + "\x00" + y
			if seen[key] {
				continue
			}
			seen[key] = true
			data.Edges = append(data.Edges, plugins.GraphEdge{Source: x, Target: y, Weight: nb.Weight})
		}
	}
	return data, nil
}

// ConversationGraph builds a graph of EVERYTHING in the current session, not
// just the message thread: a session root hub connects the conversation thread
// (turns colored by role, with the tools each turn invoked) AND everything
// attached to the session — context bases with their files, and knowledge-mode
// corpora. So "graph everything we're talking about" includes what was attached
// with /context, not only what was typed.
func (a *graphViewPluginAdapter) ConversationGraph() (plugins.GraphData, error) {
	data := &plugins.GraphData{}

	// Session root hub: everything hangs off it, so the graph has a clear center.
	rootID := "session:root"
	rootLabel := strings.TrimSpace(a.cli.currentSessionName)
	if rootLabel == "" {
		rootLabel = "session"
	}
	data.Nodes = append(data.Nodes, plugins.GraphNode{ID: rootID, Label: "◉ " + rootLabel, Kind: "session", Weight: 4})

	a.addConversationThread(data, rootID)
	a.addAttachedToGraph(data, rootID)

	// Only the root and nothing else → treat as empty so the caller shows the
	// friendly empty-graph message instead of a lone dot.
	if len(data.Nodes) <= 1 {
		return plugins.GraphData{}, nil
	}
	return *data, nil
}

// addConversationThread appends the turn thread and the tools it invoked,
// anchoring the first turn to the session root.
func (a *graphViewPluginAdapter) addConversationThread(data *plugins.GraphData, rootID string) {
	history := a.cli.history
	if len(history) > graphViewMaxConversationMsgs {
		history = history[len(history)-graphViewMaxConversationMsgs:]
	}
	toolSeen := make(map[string]bool)
	prevID := ""
	idx := 0
	for _, m := range history {
		if m.Role == "system" || m.Role == "tool" {
			continue
		}
		content := strings.TrimSpace(m.Content)
		if content == "" && len(m.ToolCalls) == 0 {
			continue
		}
		id := "msg:" + strconv.Itoa(idx)
		data.Nodes = append(data.Nodes, plugins.GraphNode{
			ID:      id,
			Label:   graphViewTurnLabel(m.Role, content),
			Kind:    m.Role,
			Summary: truncateForLog(content, 400),
			Weight:  1,
		})
		anchor := prevID
		if anchor == "" {
			anchor = rootID // first turn attaches to the session hub
		}
		data.Edges = append(data.Edges, plugins.GraphEdge{Source: anchor, Target: id, Weight: 1})
		prevID = id

		for _, tc := range m.ToolCalls {
			name := strings.TrimSpace(tc.Name)
			if name == "" {
				continue
			}
			toolID := "tool:" + name
			if !toolSeen[name] {
				toolSeen[name] = true
				data.Nodes = append(data.Nodes, plugins.GraphNode{ID: toolID, Label: name, Kind: "tool", Weight: 2})
			}
			data.Edges = append(data.Edges, plugins.GraphEdge{Source: id, Target: toolID, Weight: 1})
		}
		idx++
	}
}

// addAttachedToGraph appends the session's attached context bases (with their
// files) and knowledge-mode corpora, all linked to the session root.
func (a *graphViewPluginAdapter) addAttachedToGraph(data *plugins.GraphData, rootID string) {
	if a.cli.contextHandler == nil {
		return
	}
	mgr := a.cli.contextHandler.GetManager()
	if mgr == nil {
		return
	}
	sessionID := a.cli.currentSessionName
	if sessionID == "" {
		sessionID = "default"
	}

	contexts, _ := mgr.GetAttachedContexts(sessionID)
	for _, fc := range contexts {
		if fc == nil {
			continue
		}
		cid := "ctx:" + fc.ID
		summary := fc.Description
		if summary == "" {
			summary = fmt.Sprintf("%d file(s), %.2f MB", fc.FileCount, float64(fc.TotalSize)/1024/1024)
		}
		data.Nodes = append(data.Nodes, plugins.GraphNode{ID: cid, Label: "📎 " + fc.Name, Kind: "context", Summary: summary, Weight: 3})
		data.Edges = append(data.Edges, plugins.GraphEdge{Source: rootID, Target: cid, Weight: 2})

		for i, f := range fc.Files {
			if i >= graphViewMaxFilesPerContext {
				moreID := cid + ":more"
				data.Nodes = append(data.Nodes, plugins.GraphNode{
					ID: moreID, Label: fmt.Sprintf("+%d more files", len(fc.Files)-graphViewMaxFilesPerContext), Kind: "file", Weight: 1,
				})
				data.Edges = append(data.Edges, plugins.GraphEdge{Source: cid, Target: moreID, Weight: 1})
				break
			}
			fid := cid + ":file:" + f.Path
			data.Nodes = append(data.Nodes, plugins.GraphNode{ID: fid, Label: filepath.Base(f.Path), Kind: "file", Summary: f.Path, Weight: 1})
			data.Edges = append(data.Edges, plugins.GraphEdge{Source: cid, Target: fid, Weight: 1})
		}
	}

	for _, kb := range mgr.AttachedKnowledge(sessionID) {
		if kb == nil {
			continue
		}
		kid := "kb:" + kb.ID
		data.Nodes = append(data.Nodes, plugins.GraphNode{
			ID:      kid,
			Label:   "📚 " + kb.Name,
			Kind:    "knowledge",
			Summary: fmt.Sprintf("%d passage(s), %.2f MB indexed", kb.FileCount, float64(kb.TotalSize)/1024/1024),
			Weight:  3,
		})
		data.Edges = append(data.Edges, plugins.GraphEdge{Source: rootID, Target: kid, Weight: 2})
	}
}

// graphViewTurnLabel renders a short, role-prefixed label for a turn node.
func graphViewTurnLabel(role, content string) string {
	first := content
	if i := strings.IndexByte(first, '\n'); i >= 0 {
		first = first[:i]
	}
	first = strings.TrimSpace(first)
	prefix := "•"
	switch role {
	case "user":
		prefix = "▸"
	case "assistant":
		prefix = "◆"
	}
	if first == "" {
		first = "(tool call)"
	}
	return prefix + " " + truncateForLog(first, 48)
}
