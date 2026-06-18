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
	"sync"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/imagegen"
	"github.com/diillson/chatcli/models"
)

// generatedImagesRegistry records the file paths of images produced by the
// most recent @image gen/edit calls, so an out-of-band consumer (the messaging
// gateway) can attach the picture to its reply. It is a small bounded buffer
// guarded by a mutex; the consumer drains it with TakeGeneratedImages. In
// interactive use nothing drains it, so it self-bounds at maxRecordedImages.
var generatedImagesRegistry struct {
	mu    sync.Mutex
	paths []string
}

const maxRecordedImages = 16

// RecordGeneratedImages appends freshly written image paths to the registry.
func RecordGeneratedImages(paths ...string) {
	if len(paths) == 0 {
		return
	}
	generatedImagesRegistry.mu.Lock()
	defer generatedImagesRegistry.mu.Unlock()
	generatedImagesRegistry.paths = append(generatedImagesRegistry.paths, paths...)
	if n := len(generatedImagesRegistry.paths); n > maxRecordedImages {
		generatedImagesRegistry.paths = generatedImagesRegistry.paths[n-maxRecordedImages:]
	}
}

// TakeGeneratedImages returns and clears the recorded image paths.
func TakeGeneratedImages() []string {
	generatedImagesRegistry.mu.Lock()
	defer generatedImagesRegistry.mu.Unlock()
	out := generatedImagesRegistry.paths
	generatedImagesRegistry.paths = nil
	return out
}

// BuiltinImagePlugin is the @image tool.
type BuiltinImagePlugin struct{}

// NewBuiltinImagePlugin returns a ready-to-register plugin.
func NewBuiltinImagePlugin() *BuiltinImagePlugin { return &BuiltinImagePlugin{} }

// Name returns "@image".
func (*BuiltinImagePlugin) Name() string { return "@image" }

// Description surfaces the tool.
func (*BuiltinImagePlugin) Description() string {
	return "Generate OR edit images using the configured backend (self-hosted Stable Diffusion WebUI, a compatible endpoint, or OpenAI) and save them to file. Use 'gen' when asked to 'generate an image of', 'create a picture', 'draw', 'make an illustration'; use 'edit' when asked to 'edit', 'modify', 'change', 'alter' an existing image (image-to-image / img2img)."
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
  edit {prompt, image, images?, mask?, strength?, size?, n?, out?}
       prompt    how to transform the image (required)
       image     path to the input image (required; or "images" for several)
       mask      optional path to a PNG mask for inpainting
       strength  optional 0..1 — how much to change the input (img2img)
       out       optional output file path or directory
  status  show the effective image backend
  models  list image-capable models (catalog + your OpenAI account)`
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
				"name":        "edit",
				"description": "Edit/transform an existing image (image-to-image) guided by a prompt.",
				"flags": []map[string]interface{}{
					{"name": "prompt", "type": "string", "required": true, "description": "How to transform the image."},
					{"name": "image", "type": "string", "required": true, "description": "Path to the input image."},
					{"name": "strength", "type": "number", "required": false, "description": "0..1 how much to change the input (img2img)."},
					{"name": "out", "type": "string", "required": false, "description": "Output file or directory."},
				},
				"examples": []string{`{"cmd":"edit","args":{"prompt":"make it look like winter","image":"/tmp/photo.png"}}`},
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

// ExecuteWithStream runs the generation. Progress feedback is the agent loop's
// animated spinner (this tool is blocking, not streaming).
func (p *BuiltinImagePlugin) ExecuteWithStream(ctx context.Context, args []string, _ func(string)) (string, error) {
	if len(args) == 0 {
		return "", errors.New(`@image: empty args. Example: <tool_call name="@image" args='{"cmd":"gen","args":{"prompt":"..."}}' />`)
	}
	cmd, inner, err := parseImageInvocation(args)
	if err != nil {
		return "", fmt.Errorf("@image: %w", err)
	}

	provider := imagegen.NewFromEnvContext(ctx, nil)

	switch cmd {
	case "status":
		if imagegen.IsNull(provider) {
			return "@image: no image backend configured. Set CHATCLI_IMAGE_PROVIDER=sdwebui (self-hosted Stable Diffusion), CHATCLI_IMAGE_URL, or OPENAI_API_KEY.", nil
		}
		return "@image backend: " + provider.Name(), nil
	case "models":
		return imageModelsList(ctx), nil
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
		// No progress line here: the agent loop runs an animated spinner during
		// execution (a streamed line would stop it, then the blocking call would
		// look frozen). The spinner's label already names the operation.
		images, err := provider.Generate(ctx, in.Prompt, imagegen.Options{Size: in.Size, N: in.N})
		if err != nil {
			return "", fmt.Errorf("@image: %w", err)
		}
		paths, err := writeImages(in.Out, images)
		if err != nil {
			return "", fmt.Errorf("@image: %w", err)
		}
		RecordGeneratedImages(paths...)
		return i18n.T("image.tool.generated", len(paths), provider.Name(), strings.Join(paths, "\n  ")), nil
	case "edit":
		var in struct {
			Prompt   string   `json:"prompt"`
			Image    string   `json:"image"`
			Images   []string `json:"images"`
			Mask     string   `json:"mask"`
			Size     string   `json:"size"`
			N        int      `json:"n"`
			Strength float64  `json:"strength"`
			Out      string   `json:"out"`
		}
		_ = json.Unmarshal([]byte(inner), &in)
		if strings.TrimSpace(in.Prompt) == "" {
			return "", errors.New(`@image edit: "prompt" is required`)
		}
		paths := in.Images
		if len(paths) == 0 && strings.TrimSpace(in.Image) != "" {
			paths = []string{in.Image}
		}
		if len(paths) == 0 {
			return "", errors.New(`@image edit: "image" (path) is required`)
		}
		if imagegen.IsNull(provider) {
			return "", imagegen.ErrDisabled
		}
		// Editing inherits the configured backend (the one /model-image picks).
		// Only if that backend can't edit does ResolveEditor route to an
		// edit-capable fallback — reported below so the switch is explicit.
		editor, used, fellBack, ok := imagegen.ResolveEditor(ctx, provider, nil)
		if !ok {
			return "", fmt.Errorf("@image: backend %q does not support image editing and no edit-capable fallback is configured. Editing backends: sdwebui (img2img, keyless), openai (gpt-image-1), google (Gemini image), bedrock (Stability/Nova). Set CHATCLI_IMAGE_EDIT_PROVIDER or switch the image provider. Generation-only: xai, zai, minimax", provider.Name())
		}
		inputs, err := loadInputImages(paths)
		if err != nil {
			return "", fmt.Errorf("@image edit: %w", err)
		}
		opts := imagegen.EditOptions{Size: in.Size, N: in.N, Strength: in.Strength}
		if strings.TrimSpace(in.Mask) != "" {
			if mask, mErr := os.ReadFile(in.Mask); mErr == nil {
				opts.Mask = mask
			}
		}
		images, err := editor.Edit(ctx, in.Prompt, inputs, opts)
		if err != nil {
			return "", fmt.Errorf("@image: %w", err)
		}
		outPaths, err := writeImages(in.Out, images)
		if err != nil {
			return "", fmt.Errorf("@image: %w", err)
		}
		RecordGeneratedImages(outPaths...)
		msg := i18n.T("image.tool.edited", len(outPaths), used, strings.Join(outPaths, "\n  "))
		if fellBack {
			// Configured backend can't edit; we routed to one that can.
			msg = i18n.T("image.edit.routed", provider.Name(), used) + "\n" + msg
		}
		return msg, nil
	default:
		return "", fmt.Errorf("@image: unknown cmd %q (valid: gen|edit|status|models)", cmd)
	}
}

// loadInputImages reads the given file paths into imagegen.Image values,
// inferring the MIME/extension from the bytes.
func loadInputImages(paths []string) ([]imagegen.Image, error) {
	out := make([]imagegen.Image, 0, len(paths))
	for _, p := range paths {
		data, err := os.ReadFile(p) //#nosec G304 -- user/agent-specified image path for @image edit
		if err != nil {
			return nil, fmt.Errorf("read %q: %w", p, err)
		}
		mime, ok := models.DetectImageMediaType(data)
		if !ok {
			return nil, fmt.Errorf("%q is not a supported image", p)
		}
		ext := strings.TrimPrefix(filepath.Ext(p), ".")
		if ext == "" {
			ext = strings.TrimPrefix(mime, "image/")
		}
		out = append(out, imagegen.Image{Data: data, Mime: mime, Ext: ext})
	}
	if len(out) == 0 {
		return nil, errors.New("no readable input images")
	}
	return out, nil
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
			if !isFlatArgs(raw) {
				return "", "", fmt.Errorf("missing or unknown cmd %q (valid: gen|status)", cmdStr)
			}
			canon = "gen" // flat native args, e.g. {"prompt":"..."}
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
	if len(args) == 0 {
		return "", "", fmt.Errorf("empty args")
	}
	canon := canonicalImageCmd(args[0])
	if canon == "" {
		return "", "", fmt.Errorf("expected JSON envelope or subcommand; got %q", args[0])
	}
	return canon, argvInner(args[1:], "prompt", nil, map[string]bool{"n": true}), nil
}

func canonicalImageCmd(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "gen", "generate", "create", "draw", "image":
		return "gen"
	case "edit", "img2img", "modify", "alter", "change":
		return "edit"
	case "status", "backend":
		return "status"
	case "models", "catalog", "list":
		return "models"
	}
	return ""
}

// imageModelsList renders the static image-model catalog and, when an OpenAI key
// is present, the live image-capable models from the account.
func imageModelsList(ctx context.Context) string {
	var b strings.Builder
	b.WriteString("Image-capable models (catalog):\n")
	for _, m := range imagegen.KnownModels() {
		fmt.Fprintf(&b, "  • %-26s %-8s %-9s %s\n", m.Name, m.Provider, m.API, m.Note)
	}
	if key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); key != "" {
		if ids, err := imagegen.FetchOpenAIModels(ctx, "", key, nil); err == nil && len(ids) > 0 {
			b.WriteString("\nAvailable on your OpenAI account:\n  ")
			b.WriteString(strings.Join(ids, ", "))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
