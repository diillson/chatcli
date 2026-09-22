/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package pulse

import (
	"sort"
	"time"
)

// Node is the reduced state of one graph node: what the dashboard page
// holds for a card, computed here from the same events so a reader with no
// browser (the @dash tool, a test) sees the same numbers.
//
// Session and agent events describe one node each (Single); every other
// kind is a hub aggregated by name, counting the calls that started, are
// still open, ended in error and how long they took.
type Node struct {
	Kind   Kind
	ID     string
	Name   string
	Parent string
	Status string
	Single bool

	Calls   int
	Active  int
	Errors  int
	TotalMS int64
	Timed   int
	Attrs   map[string]string
	Last    time.Time
	Ended   bool
}

// AvgMS is the mean duration of the timed calls, 0 when none ended.
func (n *Node) AvgMS() int64 {
	if n == nil || n.Timed == 0 {
		return 0
	}
	return n.TotalMS / int64(n.Timed)
}

// Summary is the graph reduced from a run of events.
type Summary struct {
	Nodes  []*Node // stable order: kind, then name
	Events int
	First  time.Time
	Last   time.Time
	Errors []Event // the most recent events that ended in error, newest first
}

// summaryErrorsKept bounds the error tail a summary carries.
const summaryErrorsKept = 10

// kindRank orders kinds the way the dashboard lays them out.
var kindRank = map[Kind]int{
	KindSession: 0, KindAgent: 1, KindTurn: 2, KindLLM: 3, KindTool: 4, KindSkill: 5,
	KindMCP: 6, KindPattern: 7, KindBackground: 8, KindConn: 9, KindRPC: 10,
}

// Summarize reduces events, oldest first, into the graph they describe.
// Snapshot events (replayed state) count like live ones, as on the page.
func Summarize(events []Event) Summary {
	nodes := make(map[string]*Node)
	var sum Summary
	for _, ev := range events {
		sum.Events++
		if sum.First.IsZero() || ev.TS.Before(sum.First) {
			sum.First = ev.TS
		}
		if ev.TS.After(sum.Last) {
			sum.Last = ev.TS
		}
		key, single := nodeKey(ev)
		n := nodes[key]
		if n == nil {
			n = &Node{Kind: ev.Kind, ID: ev.ID, Name: ev.Name, Parent: ev.Parent, Single: single, Attrs: map[string]string{}}
			if n.Name == "" {
				n.Name = ev.ID
			}
			nodes[key] = n
		}
		if ev.Name != "" && single {
			n.Name = ev.Name
		}
		if ev.Parent != "" {
			n.Parent = ev.Parent
		}
		for k, v := range ev.Attrs {
			if k != "snapshot" {
				n.Attrs[k] = v
			}
		}
		n.Last = ev.TS
		if single {
			applySingle(n, ev)
		} else {
			applyHub(n, ev)
		}
		if ev.Status == StatusError {
			sum.Errors = append([]Event{ev}, sum.Errors...)
			if len(sum.Errors) > summaryErrorsKept {
				sum.Errors = sum.Errors[:summaryErrorsKept]
			}
		}
	}
	sum.Nodes = make([]*Node, 0, len(nodes))
	for _, n := range nodes {
		sum.Nodes = append(sum.Nodes, n)
	}
	sort.SliceStable(sum.Nodes, func(i, j int) bool {
		a, b := sum.Nodes[i], sum.Nodes[j]
		if kindRank[a.Kind] != kindRank[b.Kind] {
			return kindRank[a.Kind] < kindRank[b.Kind]
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})
	return sum
}

// ByKind returns the nodes of one kind, in summary order.
func (s Summary) ByKind(kind Kind) []*Node {
	var out []*Node
	for _, n := range s.Nodes {
		if n.Kind == kind {
			out = append(out, n)
		}
	}
	return out
}

// Session returns the process node, nil before its snapshot arrived.
func (s Summary) Session() *Node {
	for _, n := range s.Nodes {
		if n.Kind == KindSession {
			return n
		}
	}
	return nil
}

func nodeKey(ev Event) (key string, single bool) {
	switch ev.Kind {
	case KindSession:
		return "session", true
	case KindAgent:
		return "a|" + ev.ID, true
	}
	name := ev.Name
	if name == "" {
		name = ev.ID
	}
	return "h|" + string(ev.Kind) + "|" + name, false
}

func applySingle(n *Node, ev Event) {
	if ev.Status != "" {
		n.Status = ev.Status
	}
	if ev.Phase == PhaseEnd {
		n.Ended, n.Active = true, 0
		if ev.DurMS > 0 {
			n.TotalMS = ev.DurMS
		}
	} else if ev.Phase != PhasePoint {
		n.Ended, n.Active = false, 1
	}
	if ev.Status == StatusError {
		n.Errors = 1
	}
}

func applyHub(n *Node, ev Event) {
	switch ev.Phase {
	case PhaseStart:
		n.Active++
		n.Calls++
	case PhasePoint:
		n.Calls++
	case PhaseEnd:
		if n.Active > 0 {
			n.Active--
		}
		if ev.DurMS > 0 {
			n.TotalMS += ev.DurMS
			n.Timed++
		}
		if ev.Status == StatusError {
			n.Errors++
		}
	}
	switch {
	case n.Active > 0:
		n.Status = StatusRunning
	case ev.Status != "":
		n.Status = ev.Status
	}
}
