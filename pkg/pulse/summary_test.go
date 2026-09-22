/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package pulse

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSummarizeReducesHubsAndSingleNodesLikeThePage(t *testing.T) {
	t0 := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	events := []Event{
		{TS: at(0), Kind: KindSession, Phase: PhaseStart, ID: "session", Name: "chatcli", Status: StatusRunning,
			Attrs: map[string]string{"model": "m1", "provider": "P", "snapshot": "true"}},
		{TS: at(1), Kind: KindAgent, Phase: PhaseStart, ID: "run-1", Parent: "session", Name: "coder", Status: StatusRunning},
		{TS: at(2), Kind: KindTool, Phase: PhaseStart, ID: "t1", Parent: "run-1", Name: "@read", Status: StatusRunning},
		{TS: at(3), Kind: KindTool, Phase: PhaseEnd, ID: "t1", Parent: "run-1", Name: "@read", Status: StatusOK, DurMS: 120},
		{TS: at(4), Kind: KindTool, Phase: PhaseStart, ID: "t2", Parent: "run-1", Name: "@read", Status: StatusRunning},
		{TS: at(5), Kind: KindTool, Phase: PhaseStart, ID: "t3", Parent: "run-1", Name: "@shell", Status: StatusRunning},
		{TS: at(6), Kind: KindTool, Phase: PhaseEnd, ID: "t3", Parent: "run-1", Name: "@shell", Status: StatusError, DurMS: 3000},
		{TS: at(7), Kind: KindSkill, Phase: PhasePoint, ID: "s1", Parent: "run-1", Name: "golang", Status: StatusOK},
		{TS: at(8), Kind: KindSession, Phase: PhaseUpdate, ID: "session", Status: StatusRunning,
			Attrs: map[string]string{"model": "m2", "route": "override", "cost": "$0.10"}},
		{TS: at(9), Kind: KindAgent, Phase: PhaseEnd, ID: "run-1", Parent: "session", Name: "coder", Status: StatusOK, DurMS: 8000,
			Attrs: map[string]string{"turn": "3", "max_turns": "30"}},
	}
	sum := Summarize(events)

	assert.Equal(t, 10, sum.Events)
	assert.Equal(t, at(0), sum.First)
	assert.Equal(t, at(9), sum.Last)

	session := sum.Session()
	require.NotNil(t, session)
	assert.True(t, session.Single)
	assert.Equal(t, "m2", session.Attrs["model"], "updates merge attributes")
	assert.Equal(t, "P", session.Attrs["provider"], "earlier attributes survive")
	assert.Equal(t, "override", session.Attrs["route"])
	assert.NotContains(t, session.Attrs, "snapshot", "the replay marker is not state")

	agents := sum.ByKind(KindAgent)
	require.Len(t, agents, 1)
	assert.Equal(t, "run-1", agents[0].ID)
	assert.Equal(t, StatusOK, agents[0].Status)
	assert.True(t, agents[0].Ended)
	assert.Equal(t, int64(8000), agents[0].TotalMS)
	assert.Equal(t, "3", agents[0].Attrs["turn"])

	tools := sum.ByKind(KindTool)
	require.Len(t, tools, 2, "hubs aggregate by name")
	read, shell := tools[0], tools[1]
	assert.Equal(t, "@read", read.Name)
	assert.Equal(t, 2, read.Calls)
	assert.Equal(t, 1, read.Active, "one @read is still open")
	assert.Equal(t, StatusRunning, read.Status)
	assert.Equal(t, int64(120), read.AvgMS())
	assert.Equal(t, "@shell", shell.Name)
	assert.Equal(t, 1, shell.Calls)
	assert.Equal(t, 0, shell.Active)
	assert.Equal(t, 1, shell.Errors)
	assert.Equal(t, StatusError, shell.Status)
	assert.Equal(t, int64(3000), shell.AvgMS())

	skills := sum.ByKind(KindSkill)
	require.Len(t, skills, 1)
	assert.Equal(t, 1, skills[0].Calls, "a point counts as a call")
	assert.Equal(t, 0, skills[0].Active)

	require.Len(t, sum.Errors, 1)
	assert.Equal(t, "@shell", sum.Errors[0].Name)

	// Order: session, agent, tool, skill — kind rank, then name.
	kinds := make([]Kind, 0, len(sum.Nodes))
	for _, n := range sum.Nodes {
		kinds = append(kinds, n.Kind)
	}
	assert.Equal(t, []Kind{KindSession, KindAgent, KindTool, KindTool, KindSkill}, kinds)
}

func TestSummarizeKeepsOnlyTheNewestErrors(t *testing.T) {
	var events []Event
	for i := 0; i < summaryErrorsKept+5; i++ {
		events = append(events, Event{TS: time.Unix(int64(i), 0), Kind: KindConn, Phase: PhaseEnd, ID: "c", Name: "host", Status: StatusError})
	}
	sum := Summarize(events)
	require.Len(t, sum.Errors, summaryErrorsKept)
	assert.Equal(t, time.Unix(int64(summaryErrorsKept+4), 0), sum.Errors[0].TS, "newest first")
	assert.Equal(t, summaryErrorsKept+5, sum.ByKind(KindConn)[0].Errors)
}

func TestSummarizeEmpty(t *testing.T) {
	sum := Summarize(nil)
	assert.Equal(t, 0, sum.Events)
	assert.Nil(t, sum.Session())
	assert.Empty(t, sum.Nodes)
	var none *Node
	assert.Equal(t, int64(0), none.AvgMS())
}
