/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * @view — bring a local image into the model's own eyes mid-task: a
 * screenshot the @browser just captured, a design mock, a diagram, a chart.
 * The image is loaded, gated through the session's vision pipeline (native
 * multimodal when the model supports it, describe-fallback otherwise) and
 * attached to the conversation at the next turn boundary, so the model SEES
 * it instead of guessing from file names.
 *
 * Same adapter seam as the other builtins: the cli package wires the vision
 * pipeline and the agent-loop staging; the plugin stays a thin dispatcher.
 */
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
)

// ViewAdapter is the surface @view needs from the live session. The returned
// string is pre-rendered, model-facing text.
type ViewAdapter interface {
	// ViewImage loads path and routes it through the vision pipeline:
	// staged as a native image attachment, or described in text when the
	// model has no vision.
	ViewImage(ctx context.Context, path string) (string, error)
}

type viewAdapterHolder struct{ a ViewAdapter }

var viewAdapterAtom atomic.Value // viewAdapterHolder

// SetViewAdapter wires the live adapter. Called from the top-level cli
// package at startup; pass nil to detach.
func SetViewAdapter(a ViewAdapter) {
	viewAdapterAtom.Store(viewAdapterHolder{a: a})
}

func currentViewAdapter() ViewAdapter {
	v := viewAdapterAtom.Load()
	if v == nil {
		return nil
	}
	h, _ := v.(viewAdapterHolder)
	return h.a
}

// BuiltinViewPlugin is the @view tool.
type BuiltinViewPlugin struct{}

// NewBuiltinViewPlugin returns a ready-to-register plugin.
func NewBuiltinViewPlugin() *BuiltinViewPlugin { return &BuiltinViewPlugin{} }

// Name returns "@view".
func (*BuiltinViewPlugin) Name() string { return "@view" }

// Description surfaces the tool in /plugin list and the agent tool catalog.
func (*BuiltinViewPlugin) Description() string {
	return "Look at a local image file with your own eyes: screenshots (pairs with @browser screenshot), design mocks, diagrams, charts, UI states. The image is attached to the conversation through the session's vision pipeline — native multimodal when the model supports it, a text description otherwise. Use it whenever what a file LOOKS like matters; reading bytes with @coder read cannot show you a picture."
}

// Usage explains the canonical invocation forms.
func (*BuiltinViewPlugin) Usage() string {
	return `<tool_call name="@view" args='{"cmd":"view","args":{"file":"/tmp/chatcli-browser/screenshot-1.png"}}' />

Subcommands:
  view {file}   attach a local image (png, jpeg, gif, webp) to the conversation

The image lands on the NEXT turn: request it, then analyze what is visible.
PDFs are not supported yet — render the relevant page to an image first.`
}

// Version returns the plugin contract version.
func (*BuiltinViewPlugin) Version() string { return "1.0.0" }

// Path identifies the plugin as builtin.
func (*BuiltinViewPlugin) Path() string { return "[builtin]" }

// Schema declares the machine-readable command surface.
func (*BuiltinViewPlugin) Schema() string {
	schema := map[string]interface{}{
		"name":        "@view",
		"description": "Attach a local image to the conversation through the vision pipeline.",
		"commands": []map[string]interface{}{
			{"name": "view", "description": "attach a local image (png, jpeg, gif, webp)", "examples": []string{`{"cmd":"view","args":{"file":"shot.png"}}`}},
		},
	}
	b, _ := json.MarshalIndent(schema, "", "  ")
	return string(b)
}

// Execute dispatches a @view invocation.
func (p *BuiltinViewPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream dispatches a @view invocation (no streaming).
func (p *BuiltinViewPlugin) ExecuteWithStream(ctx context.Context, args []string, _ func(string)) (string, error) {
	adapter := currentViewAdapter()
	if adapter == nil {
		return "", errors.New("@view: no vision adapter wired in this session")
	}
	path, err := parseViewInvocation(args)
	if err != nil {
		return "", err
	}
	out, err := adapter.ViewImage(ctx, path)
	if err != nil {
		return "", fmt.Errorf("@view: %w", err)
	}
	return out, nil
}

// parseViewInvocation accepts the JSON envelope, "view <path>" and a bare
// path — lenient like every other builtin.
func parseViewInvocation(args []string) (string, error) {
	if len(args) == 0 {
		return "", errors.New(`@view: empty args. Example: <tool_call name="@view" args='{"cmd":"view","args":{"file":"shot.png"}}' />`)
	}
	payload := strings.TrimSpace(strings.Join(args, " "))
	if strings.HasPrefix(payload, "{") {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(payload), &raw); err != nil {
			return "", fmt.Errorf(`@view: parse envelope: %w. Expected {"cmd":"view","args":{"file":"..."}}`, err)
		}
		inner := raw
		if ra, ok := raw["args"]; ok && len(ra) > 0 && strings.TrimSpace(string(ra)) != "null" {
			var nested map[string]json.RawMessage
			if err := json.Unmarshal(ra, &nested); err == nil {
				inner = nested
			}
		}
		for _, key := range []string{"file", "path", "image"} {
			if v, ok := inner[key]; ok {
				var s string
				if json.Unmarshal(v, &s) == nil && strings.TrimSpace(s) != "" {
					return strings.TrimSpace(s), nil
				}
			}
		}
		return "", errors.New(`@view: missing file. Example: {"cmd":"view","args":{"file":"shot.png"}}`)
	}
	rest := args
	if strings.EqualFold(args[0], "view") {
		rest = args[1:]
	}
	// The agent loop flattens {"cmd":"view","args":{"file":X}} into argv
	// ["view","--file",X]; pull the value out of the --file/--path/--image
	// flag so it is not mistaken for a literal path like "--file X".
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		for _, fl := range []string{"--file", "--path", "--image"} {
			if a == fl && i+1 < len(rest) {
				return strings.TrimSpace(rest[i+1]), nil
			}
			if v, ok := strings.CutPrefix(a, fl+"="); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v), nil
			}
		}
	}
	if len(rest) == 0 || strings.TrimSpace(rest[0]) == "" || strings.HasPrefix(rest[0], "--") {
		return "", errors.New("@view: missing file path")
	}
	return strings.TrimSpace(strings.Join(rest, " ")), nil
}
