/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package remote

import (
	"context"
	"errors"
	"fmt"
	"io"

	pb "github.com/diillson/chatcli/proto/chatcli/v1"
)

// PipelineOpts are the per-call routing overrides for the pipeline RPCs.
type PipelineOpts struct {
	Provider string
	Model    string
	// Quality maps CHATCLI_QUALITY_* overrides for a coder/agent run.
	Quality map[string]string
	// Plain asks ChatTurn for the bare passthrough (no enrichment).
	Plain bool
}

// PipelineTool describes one tool the server's pipeline exposes.
type PipelineTool struct {
	Name        string
	Description string
	Usage       string
	Schema      string
	ReadOnly    bool
}

// ChatTurn runs one full-pipeline chat turn on the server (memory, contexts,
// skills, knowledge and compaction on the server side). Returns the reply
// and the namespaced session id the server used.
func (c *Client) ChatTurn(ctx context.Context, session, text string, opts PipelineOpts) (string, string, error) {
	resp, err := c.grpcClient.ChatTurn(c.withAuth(ctx), &pb.ChatTurnRequest{
		Session: session, Text: text, Provider: opts.Provider, Model: opts.Model, Plain: opts.Plain,
	})
	if err != nil {
		return "", "", fmt.Errorf("remote ChatTurn failed: %w", err)
	}
	return resp.GetReply(), resp.GetSession(), nil
}

// RunCoder runs the coder loop on the server. onLine receives transcript
// lines as the loop works; the final answer is returned.
func (c *Client) RunCoder(ctx context.Context, session, task string, opts PipelineOpts, onLine func(string)) (string, error) {
	stream, err := c.grpcClient.RunCoder(c.withAuth(ctx), pipelineTaskRequest(session, task, opts))
	if err != nil {
		return "", fmt.Errorf("remote RunCoder failed: %w", err)
	}
	return drainTaskStream(stream, onLine)
}

// RunAgent runs the agent loop on the server with the same contract.
func (c *Client) RunAgent(ctx context.Context, session, task string, opts PipelineOpts, onLine func(string)) (string, error) {
	stream, err := c.grpcClient.RunAgent(c.withAuth(ctx), pipelineTaskRequest(session, task, opts))
	if err != nil {
		return "", fmt.Errorf("remote RunAgent failed: %w", err)
	}
	return drainTaskStream(stream, onLine)
}

// ListPipelineTools lists the tools the server's pipeline exposes.
func (c *Client) ListPipelineTools(ctx context.Context) ([]PipelineTool, error) {
	resp, err := c.grpcClient.ListPipelineTools(c.withAuth(ctx), &pb.ListPipelineToolsRequest{})
	if err != nil {
		return nil, fmt.Errorf("remote ListPipelineTools failed: %w", err)
	}
	out := make([]PipelineTool, 0, len(resp.GetTools()))
	for _, t := range resp.GetTools() {
		out = append(out, PipelineTool{Name: t.GetName(), Description: t.GetDescription(), Usage: t.GetUsage(), Schema: t.GetSchema(), ReadOnly: t.GetReadOnly()})
	}
	return out, nil
}

// RunPipelineTool invokes one tool on the server with the raw argument string.
func (c *Client) RunPipelineTool(ctx context.Context, name, args string) (string, error) {
	resp, err := c.grpcClient.RunPipelineTool(c.withAuth(ctx), &pb.RunPipelineToolRequest{Name: name, Args: args})
	if err != nil {
		return "", fmt.Errorf("remote RunPipelineTool failed: %w", err)
	}
	return resp.GetOutput(), nil
}

func pipelineTaskRequest(session, task string, opts PipelineOpts) *pb.PipelineTaskRequest {
	return &pb.PipelineTaskRequest{Session: session, Task: task, Provider: opts.Provider, Model: opts.Model, Quality: opts.Quality}
}

// taskEventStream is what the RunCoder and RunAgent client streams share.
type taskEventStream interface {
	Recv() (*pb.PipelineTaskEvent, error)
}

// drainTaskStream forwards lines and returns the final answer. A stream that
// closes without a final event yields what was collected so far.
func drainTaskStream(stream taskEventStream, onLine func(string)) (string, error) {
	for {
		ev, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return "", nil
			}
			return "", fmt.Errorf("remote task stream failed: %w", err)
		}
		switch e := ev.GetEvent().(type) {
		case *pb.PipelineTaskEvent_Line:
			if onLine != nil {
				onLine(e.Line)
			}
		case *pb.PipelineTaskEvent_Final:
			return e.Final, nil
		}
	}
}
