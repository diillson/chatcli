/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * BuiltinImagePlugin — image generation as an @image ReAct tool.
 *
 * It generates images from a text prompt using the configured backend
 * (self-hosted Stable Diffusion WebUI, an OpenAI-compatible endpoint, or
 * OpenAI), local/keyless-first, and saves them to file(s). Self-contained — it
 * reads the backend from the environment via imagegen.NewFromEnv, so no adapter
 * wiring is required.
 */
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/imagegen"
)

// BuiltinImagePlugin is the @image tool.
type BuiltinImagePlugin struct{}

// NewBuiltinImagePlugin returns a ready-to-register plugin.
func NewBuiltinImagePlugin() *BuiltinImagePlugin { return &BuiltinImagePlugin{} }

// Name returns "@image".
func (*BuiltinImagePlugin) Name() string { return "@image" }

// Description surfaces the tool.
func (*BuiltinImagePlugin) Description() string {
	return "Generate images from a text prompt using the configured backend (self-hosted Stable Diffusion WebUI, a compatible endpoint, or OpenAI) and save them to file. Use when asked to 'generate an image of', 'create a picture', 'draw', 'make an illustration'."
}

// Usage explains the canonical invocation.
func (*BuiltinImagePlugin) Usage() string {
	return `<tool_call name="@image" args='{"cmd":"gen","args":{"prompt":"a red bicycle on the moon"}}' />

Subcommands (cmd + args):
  gen {prompt, size?, n?, out?}
       prompt  the image description (required)
       size    optional WxH (default 1024x1024)
       n       optional number of images (default 1)
       out     optional output file path (single image) or directory (multiple)
  status  show the effective image backend`
}

// Version is semver.
func (*BuiltinImagePlugin) Version() string { return "1.0.0" }

// Path is empty for builtin plugins.
func (*BuiltinImagePlugin) Path() string { return "" }

// Schema describes the subcommands.
func (*BuiltinImagePlugin) Schema() string {
	schema := map[string]interface{}{
		"argsFormat": "JSON envelope {cmd, args} preferred",
		"subcommands": []map[string]interface{}{
			{
				"name":        "gen",
				"description": "Generate image(s) from a prompt and save to file.",
				"flags": []map[string]interface{}{
					{"name": "prompt", "type": "string", "required": true, "description": "Image description."},
					{"name": "size", "type": "string", "required": false, "description": "WxH, e.g. 1024x1024."},
					{"name": "n", "type": "number", "required": false, "description": "Number of images (default 1)."},
					{"name": "out", "type": "string", "required": false, "description": "Output file (single) or directory (multiple)."},
				},
				"examples": []string{`{"cmd":"gen","args":{"prompt":"a watercolor fox","size":"1024x1024"}}`},
			},
			{
				"name":        "status",
				"description": "Show the effective image backend.",
				"examples":    []string{`{"cmd":"status"}`},
			},
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}

// Execute parses args and dispatches.
func (p *BuiltinImagePlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream ignores the stream callback.
func (p *BuiltinImagePlugin) ExecuteWithStream(ctx context.Context, args []string, _ func(string)) (string, error) {
	if len(args) == 0 {
		return "", errors.New(`@image: empty args. Example: <tool_call name="@image" args='{"cmd":"gen","args":{"prompt":"..."}}' />`)
	}
	cmd, inner, err := parseImageInvocation(args)
	if err != nil {
		return "", fmt.Errorf("@image: %w", err)
	}

	provider := imagegen.NewFromEnv(nil)

	switch cmd {
	case "status":
		if imagegen.IsNull(provider) {
			return "@image: no image backend configured. Set CHATCLI_IMAGE_PROVIDER=sdwebui (self-hosted Stable Diffusion), CHATCLI_IMAGE_URL, or OPENAI_API_KEY.", nil
		}
		return "@image backend: " + provider.Name(), nil
	case "gen":
		var in struct {
			Prompt string `json:"prompt"`
			Size   string `json:"size"`
			N      int    `json:"n"`
			Out    string `json:"out"`
		}
		_ = json.Unmarshal([]byte(inner), &in)
		if strings.TrimSpace(in.Prompt) == "" {
			return "", errors.New(`@image gen: "prompt" is required`)
		}
		if imagegen.IsNull(provider) {
			return "", imagegen.ErrDisabled
		}
		images, err := provider.Generate(ctx, in.Prompt, imagegen.Options{Size: in.Size, N: in.N})
		if err != nil {
			return "", fmt.Errorf("@image: %w", err)
		}
		paths, err := writeImages(in.Out, images)
		if err != nil {
			return "", fmt.Errorf("@image: %w", err)
		}
		return i18n.T("image.tool.generated", len(paths), provider.Name(), strings.Join(paths, "\n  ")), nil
	default:
		return "", fmt.Errorf("@image: unknown cmd %q (valid: gen|status)", cmd)
	}
}

// writeImages saves the images. With one image and an out file path, it writes
// there; otherwise it writes into out (a directory) or temp files.
func writeImages(out string, images []imagegen.Image) ([]string, error) {
	paths := make([]string, 0, len(images))
	if len(images) == 1 && out != "" && !isDir(out) {
		if err := os.WriteFile(out, images[0].Data, 0o600); err != nil {
			return nil, err
		}
		abs, _ := filepath.Abs(out)
		return []string{abs}, nil
	}
	for i, img := range images {
		if out != "" && isDir(out) {
			path := filepath.Join(out, fmt.Sprintf("image-%d.%s", i+1, img.Ext))
			if err := os.WriteFile(path, img.Data, 0o600); err != nil {
				return nil, err
			}
			abs, _ := filepath.Abs(path)
			paths = append(paths, abs)
			continue
		}
		f, err := os.CreateTemp("", "chatcli-image-*."+img.Ext)
		if err != nil {
			return nil, err
		}
		_, werr := f.Write(img.Data)
		_ = f.Close()
		if werr != nil {
			return nil, werr
		}
		paths = append(paths, f.Name())
	}
	return paths, nil
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func parseImageInvocation(args []string) (string, string, error) {
	payload := strings.TrimSpace(strings.Join(args, " "))
	if strings.HasPrefix(payload, "{") {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(payload), &raw); err != nil {
			return "", "", fmt.Errorf("parse envelope: %w", err)
		}
		var cmdStr string
		if rc, ok := raw["cmd"]; ok {
			_ = json.Unmarshal(rc, &cmdStr)
		}
		canon := canonicalImageCmd(cmdStr)
		if canon == "" {
			return "", "", fmt.Errorf("missing or unknown cmd %q (valid: gen|status)", cmdStr)
		}
		var inner string
		if rargs, ok := raw["args"]; ok && len(rargs) > 0 {
			inner = string(rargs)
		} else {
			delete(raw, "cmd")
			b, _ := json.Marshal(raw)
			inner = string(b)
		}
		return canon, inner, nil
	}
	canon := canonicalImageCmd(args[0])
	if canon == "" {
		return "", "", fmt.Errorf("expected JSON envelope or subcommand; got %q", args[0])
	}
	if canon == "gen" {
		rest := strings.TrimSpace(strings.TrimPrefix(payload, args[0]))
		b, _ := json.Marshal(map[string]string{"prompt": rest})
		return canon, string(b), nil
	}
	return canon, "{}", nil
}

func canonicalImageCmd(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "gen", "generate", "create", "draw", "image":
		return "gen"
	case "status", "backend":
		return "status"
	}
	return ""
}
