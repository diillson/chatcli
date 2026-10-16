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
	return "Generate OR edit images and choose the image backend/model. Backends: OpenAI (Images or Responses API), Google Gemini, xAI Grok Imagine, AWS Bedrock (Stability), Z.AI, MiniMax, self-hosted SD WebUI or a compatible endpoint. Use 'gen' for 'generate/create/draw an image of', 'edit' to modify an existing image (image-to-image, inpainting with a mask, background removal, upscaling on Bedrock), 'models' to see which models each backend offers on this account (live), 'use' to switch backend and/or model when asked (e.g. 'use Gemini for images'), 'status' for the active backend. Edits run on the active backend; one that cannot edit fails unless an edit backend is pinned with use {for:\"edit\"}."
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
  models {provider?, live?}
       per backend: which models it offers to this account (live listing
       where the provider has one, else the catalog), gen/edit support,
       shutdown dates, Bedrock regions
  use {provider?, model?, for?}
       switch the image backend and/or model for the session; with only a
       model the provider is inferred, with only a provider its default
       model is used; for:"edit" pins the backend used when the active one
       cannot edit
  status  the active backend and model, whether it edits, configured backends`
}

// Version is semver.
func (*BuiltinImagePlugin) Version() string { return "1.1.0" }

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
				"description": "Edit/transform existing image(s) with a prompt on the active backend (image-to-image; Bedrock models also inpaint with a mask, remove backgrounds, upscale).",
				"flags": []map[string]interface{}{
					{"name": "prompt", "type": "string", "required": true, "description": "How to transform the image."},
					{"name": "image", "type": "string", "required": true, "description": "Path to the input image."},
					{"name": "images", "type": "array", "required": false, "description": "Several input image paths (models that take references)."},
					{"name": "mask", "type": "string", "required": false, "description": "PNG mask path: white = area to change (inpainting)."},
					{"name": "strength", "type": "number", "required": false, "description": "0..1 how much to change the input (img2img)."},
					{"name": "size", "type": "string", "required": false, "description": "Output WxH when the backend supports it."},
					{"name": "n", "type": "number", "required": false, "description": "Number of variations."},
					{"name": "out", "type": "string", "required": false, "description": "Output file or directory."},
				},
				"examples": []string{`{"cmd":"edit","args":{"prompt":"make it look like winter","image":"/tmp/photo.png"}}`},
			},
			{
				"name":        "models",
				"description": "List image models per backend: availability on this account (live), gen/edit support, shutdown dates, Bedrock regions.",
				"flags": []map[string]interface{}{
					{"name": "provider", "type": "string", "required": false, "description": "Only this backend: openai|responses|google|xai|zai|minimax|bedrock|sdwebui|url."},
					{"name": "live", "type": "boolean", "required": false, "description": "Ask the providers' APIs (default true); false = catalog only, no network."},
				},
				"examples": []string{`{"cmd":"models"}`, `{"cmd":"models","args":{"provider":"bedrock"}}`},
			},
			{
				"name":        "use",
				"description": "Switch the image backend and/or model for this session. Only a model: the provider is inferred. Only a provider: its default model. for=edit pins the backend used for edits when the active one cannot edit.",
				"flags": []map[string]interface{}{
					{"name": "provider", "type": "string", "required": false, "description": "openai|responses|google|xai|zai|minimax|bedrock|sdwebui|url."},
					{"name": "model", "type": "string", "required": false, "description": "Model id, as @image models lists it."},
					{"name": "for", "type": "string", "required": false, "description": "gen (default) or edit."},
				},
				"examples": []string{`{"cmd":"use","args":{"model":"gemini-nano-banana-2.1"}}`, `{"cmd":"use","args":{"provider":"bedrock","model":"stability.sd3-5-large-v1:0"}}`, `{"cmd":"use","args":{"provider":"xai","for":"edit"}}`},
			},
			{
				"name":        "status",
				"description": "Active backend and model, whether it edits, the pinned edit backend, configured backends.",
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
		return ImageStatus(provider), nil
	case "models":
		var in struct {
			Provider string `json:"provider"`
			Live     *bool  `json:"live"`
		}
		_ = json.Unmarshal([]byte(inner), &in)
		live := in.Live == nil || *in.Live
		if in.Provider != "" {
			if _, ok := imagegen.LookupBackend(in.Provider); !ok {
				return "", fmt.Errorf("@image models: unknown provider %q (valid: openai, responses, google, xai, zai, minimax, bedrock, sdwebui, url)", in.Provider)
			}
		}
		return RenderImageModels(imagegen.ListModels(ctx, in.Provider, imagegen.ActiveBackend(provider), live, nil)), nil
	case "use":
		var in struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
			For      string `json:"for"`
		}
		_ = json.Unmarshal([]byte(inner), &in)
		forEdit := strings.EqualFold(strings.TrimSpace(in.For), "edit")
		sel, err := imagegen.Select(in.Provider, in.Model, forEdit)
		if err != nil {
			return "", fmt.Errorf("@image use: %w", err)
		}
		sel.Apply()
		return RenderImageSelection(sel, imagegen.NewFromEnvContext(ctx, nil)), nil
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
		images, err := imagegen.GenerateTraced(ctx, provider, in.Prompt, imagegen.Options{Size: in.Size, N: in.N})
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
		// Editing uses the configured backend (the one /model-image picks), in
		// that backend's own edit contract. A backend that can't edit fails
		// here, before any request — unless CHATCLI_IMAGE_EDIT_PROVIDER pins
		// another one, which is then named in the reply.
		editor, used, routed, ok := imagegen.ResolveEditor(ctx, provider, nil)
		if !ok {
			return "", errors.New(editUnavailableMessage(imagegen.EditUnavailable(provider), provider))
		}
		inputs, err := loadInputImages(paths)
		if err != nil {
			return "", fmt.Errorf("@image edit: %w", err)
		}
		opts := imagegen.EditOptions{Size: in.Size, N: in.N, Strength: in.Strength}
		if strings.TrimSpace(in.Mask) != "" {
			mask, mErr := os.ReadFile(in.Mask) //#nosec G304 -- user/agent-specified mask path for @image edit
			if mErr != nil {
				// An inpainting edit without its mask would repaint the
				// whole image instead of the masked area.
				return "", fmt.Errorf("@image edit: read mask: %w", mErr)
			}
			opts.Mask = mask
		}
		images, err := imagegen.EditTraced(ctx, editor, in.Prompt, inputs, opts)
		if err != nil {
			return "", fmt.Errorf("@image: %w", err)
		}
		outPaths, err := writeImages(in.Out, images)
		if err != nil {
			return "", fmt.Errorf("@image: %w", err)
		}
		RecordGeneratedImages(outPaths...)
		msg := i18n.T("image.tool.edited", len(outPaths), used, strings.Join(outPaths, "\n  "))
		if routed {
			// The configured backend can't edit; the pinned edit backend did.
			msg = i18n.T("image.edit.pinned_routed", provider.Name(), used) + "\n" + msg
		}
		return msg, nil
	default:
		return "", fmt.Errorf("@image: unknown cmd %q (valid: gen|edit|status|models|use)", cmd)
	}
}

// editUnavailableMessage explains, in the user's language, why the edit did
// not run and how to make it run.
func editUnavailableMessage(ue *imagegen.EditUnavailableError, provider imagegen.Provider) string {
	backend := ue.Backend
	if ue.Model != "" {
		backend = fmt.Sprintf("%s (%s)", ue.Backend, ue.Model)
	}
	if ue.Pinned != "" {
		return i18n.T("image.edit.pinned_unavailable", backend, ue.Pinned)
	}
	if provider.Name() == "bedrock" {
		return i18n.T("image.edit.bedrock_model", backend)
	}
	return i18n.T("image.edit.unsupported", backend)
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
				return "", "", fmt.Errorf("missing or unknown cmd %q (valid: gen|edit|status|models|use)", cmdStr)
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
	case "use", "switch", "set", "select":
		return "use"
	}
	return ""
}
