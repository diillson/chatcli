/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Package webui serves ChatCLI's local web application: a browser front end
// for the same engine the terminal, MCP, ACP and the gateway drive. It binds
// to the loopback interface with a token minted per run, streams turns to
// the page over server-sent events, and resolves the engine's permission
// dialogs from the browser. Sessions are the shared saved sessions, so a
// conversation started in the terminal continues in the browser and back.
package webui

import (
	"context"

	"github.com/diillson/chatcli/cli"
	"github.com/diillson/chatcli/cli/agentevents"
	"github.com/diillson/chatcli/cli/rpcserve"
	"github.com/diillson/chatcli/models"
)

// ImageInput is an image the page attached to a turn.
type ImageInput struct {
	Name      string
	MediaType string
	Data      []byte
}

// TurnOptions parameterize one turn as the page requested it.
type TurnOptions struct {
	Provider string
	Model    string
	Images   []ImageInput
}

// Status is what the page shows in its header and settings: the active
// route, the spend and the MCP servers.
type Status struct {
	Version       string                   `json:"version"`
	Provider      string                   `json:"provider"`
	Model         string                   `json:"model"`
	PolicyMode    string                   `json:"policy_mode"`
	MaxTokens     int                      `json:"max_tokens"`
	Cost          *cli.SessionCostData     `json:"cost,omitempty"`
	DailySpentUSD float64                  `json:"daily_spent_usd"`
	DailyLimitUSD float64                  `json:"daily_limit_usd"`
	BudgetBlocked bool                     `json:"budget_blocked"`
	MCP           []cli.MCPServerStatusRPC `json:"mcp"`
}

// Backend is the engine seam the web server drives. The shared RPC backend
// in cmd implements it, so the browser gets the same chat, coder, agent,
// tools, skills, sessions and commands the other surfaces get.
type Backend interface {
	HasLLM() bool
	// Defaults are the provider and model used when a turn names none.
	Defaults() (provider, model string)
	SetDefaults(provider, model string)
	ProvidersJSON() (string, error)

	// Chat runs one chat turn; the sink receives the reply as it streams.
	Chat(ctx context.Context, session, text string, o TurnOptions, sink cli.ChunkSink) (string, error)
	// RunAgent and RunCoder drive the loops; events stream through the sink,
	// which also answers permission requests when it implements
	// agentevents.PermissionDecider.
	RunAgent(ctx context.Context, session, task string, o TurnOptions, events agentevents.Sink) (string, error)
	RunCoder(ctx context.Context, session, task string, o TurnOptions, events agentevents.Sink) (string, error)

	Tools() []rpcserve.ToolInfo
	CallTool(ctx context.Context, name, args string) (string, error)
	Skills() []rpcserve.SkillInfo
	SkillContent(name string) (string, error)
	Commands() []rpcserve.CommandInfo
	RunCommand(ctx context.Context, session, line string) (string, error)

	ManageSession(ctx context.Context, action, session, name string) (string, error)
	RestoreSession(ctx context.Context, session string) ([]rpcserve.HistoryItem, error)
	SessionCatalog() []cli.SessionSummaryRPC
	SessionMessages(name string, offset, limit int) ([]models.Message, int, error)

	Resources() []rpcserve.ResourceInfo
	ReadResource(ctx context.Context, uri string) (rpcserve.ResourceContent, error)

	Status() Status
}
