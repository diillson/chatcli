/*
 * ChatCLI - /config image mutator.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Exposes the @image backend on the /config surface — read-only panorama plus
 * runtime mutation. The imagegen factory reads os.Getenv on every call, so each
 * setter takes effect immediately; a hint points to .env for a permanent
 * default (we never rewrite .env — user territory).
 *
 *   /config image                      # status
 *   /config image provider <name>      # sdwebui|url|openai|responses|google|xai|zai|minimax|bedrock|auto
 *   /config image api images|responses # OpenAI: Images API vs Responses API
 *   /config image model <id>           # model, and the provider that serves it
 *   /config image edit <provider|model> [model] # backend used when the active one cannot edit
 *   /config image url <url>            # CHATCLI_IMAGE_URL (self-hosted/SD WebUI)
 *   /config image models               # list the image-model catalog
 *   /config image reset                # clear the overrides above
 *
 * Shorthand: `/model-image [<id>]` mirrors `/config image model` (and lists the
 * catalog when bare), the @image counterpart to `/model` for text models.
 */
package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/imagegen"
)

// routeConfigImage dispatches /config image <sub> [args].
func (cli *ChatCLI) routeConfigImage(ctx context.Context, args []string) {
	if len(args) == 0 {
		cli.showConfigImage(ctx)
		return
	}
	switch strings.ToLower(args[0]) {
	case "help", "-h", "--help":
		cli.printConfigImageUsage()
	case "provider":
		cli.setImageEnv(ctx, "CHATCLI_IMAGE_PROVIDER", args[1:])
	case "api":
		cli.setImageEnv(ctx, "CHATCLI_IMAGE_API", args[1:])
	case "model":
		cli.selectImage(ctx, "", strings.Join(args[1:], " "), false)
	case "edit":
		cli.selectImageEdit(ctx, args[1:])
	case "url":
		cli.setImageEnv(ctx, "CHATCLI_IMAGE_URL", args[1:])
	case "models", "catalog":
		fmt.Println(cli.imageModelsCatalog(ctx))
	case "reset":
		for _, k := range []string{"CHATCLI_IMAGE_PROVIDER", "CHATCLI_IMAGE_API", "CHATCLI_IMAGE_MODEL", "CHATCLI_IMAGE_URL", "CHATCLI_IMAGE_EDIT_PROVIDER", "CHATCLI_IMAGE_EDIT_MODEL"} {
			_ = os.Unsetenv(k)
		}
		fmt.Println(colorize("  "+i18n.T("cfg.image.reset_ok"), ColorGreen))
		cli.showConfigImage(ctx)
	default:
		fmt.Println(colorize("  "+i18n.T("cfg.image.unknown_sub", args[0]), ColorYellow))
		cli.printConfigImageUsage()
	}
}

// handleImageModelCommand is the `/model-image [<id>]` shorthand for
// `/config image model <id>`: it switches only the @image generation model
// (CHATCLI_IMAGE_MODEL) without the longer /config path, mirroring how `/model`
// shortcuts the text-model switch. With no argument it prints the image-model
// catalog (same as `/config image models`). It deliberately does NOT touch the
// text/chat model — image and text models are separate backends.
func (cli *ChatCLI) handleImageModelCommand(ctx context.Context, input string) {
	rest := strings.TrimSpace(strings.TrimPrefix(input, "/model-image"))
	if rest == "" {
		fmt.Println(cli.imageModelsCatalog(ctx))
		return
	}
	cli.selectImage(ctx, "", rest, false)
}

// selectImage switches the image backend and/or model the way @image use
// does: a model alone selects the provider that serves it, so picking a
// Gemini model while the provider is pinned to xAI does not send a Gemini
// id to xAI.
func (cli *ChatCLI) selectImage(ctx context.Context, provider, model string, forEdit bool) {
	sel, err := imagegen.Select(provider, model, forEdit)
	if err != nil {
		fmt.Println(colorize("  "+err.Error(), ColorYellow))
		return
	}
	sel.Apply()
	fmt.Println(plugins.RenderImageSelection(sel, imagegen.NewFromEnvContext(ctx, cli.logger)))
}

// selectImageEdit handles `/config image edit <provider|model> [model]`.
func (cli *ChatCLI) selectImageEdit(ctx context.Context, args []string) {
	if len(args) == 0 {
		fmt.Println(colorize("  "+i18n.T("cfg.image.value_required", "CHATCLI_IMAGE_EDIT_PROVIDER"), ColorYellow))
		return
	}
	provider, model := args[0], strings.Join(args[1:], " ")
	if _, isBackend := imagegen.LookupBackend(provider); !isBackend {
		provider, model = "", strings.Join(args, " ") // a model alone
	}
	cli.selectImage(ctx, provider, model, true)
}

// getImageModelSuggestions autocompletes the `/model-image <id>` shorthand with
// the image-model catalog — the same source as `/config image model`.
func (cli *ChatCLI) getImageModelSuggestions(d prompt.Document) []prompt.Suggest {
	line := d.TextBeforeCursor()
	args := strings.Fields(line)
	word := d.GetWordBeforeCursor()

	// Just typed "/model-image" without a trailing space.
	if len(args) == 1 && !strings.HasSuffix(line, " ") {
		return []prompt.Suggest{
			{Text: "/model-image", Description: i18n.T("complete.root.model_image")},
		}
	}

	out := make([]prompt.Suggest, 0, len(imagegen.KnownModels()))
	for _, m := range imagegen.KnownModels() {
		out = append(out, prompt.Suggest{Text: m.Name, Description: m.Provider + " · " + m.API})
	}
	return prompt.FilterHasPrefix(out, word, true)
}

// setImageEnv applies a single image env override at runtime.
func (cli *ChatCLI) setImageEnv(ctx context.Context, key string, args []string) {
	if len(args) == 0 {
		fmt.Println(colorize("  "+i18n.T("cfg.image.value_required", key), ColorYellow))
		return
	}
	val := strings.TrimSpace(strings.Join(args, " "))
	_ = os.Setenv(key, val)
	fmt.Println(colorize("  "+i18n.T("cfg.image.set_ok", key, val), ColorGreen))
	cli.showConfigImage(ctx)
}

// showConfigImage renders the @image panorama.
func (cli *ChatCLI) showConfigImage(ctx context.Context) {
	sectionHeader("🎨", "cfg.section.image.title", ColorBlue)
	p := uiPrefix(ColorBlue)

	status := i18n.T("cfg.val.imagegen_off")
	if g := imagegen.NewFromEnvContext(ctx, cli.logger); !imagegen.IsNull(g) {
		status = g.Name()
	}
	kv(p, i18n.T("cfg.kv.imagegen"), status)
	kv(p, "CHATCLI_IMAGE_PROVIDER", envOr("CHATCLI_IMAGE_PROVIDER"))
	kv(p, "CHATCLI_IMAGE_API", envOr("CHATCLI_IMAGE_API"))
	kv(p, "CHATCLI_IMAGE_MODEL", envOr("CHATCLI_IMAGE_MODEL"))
	kv(p, "CHATCLI_IMAGE_URL", envOr("CHATCLI_IMAGE_URL"))
	kv(p, "CHATCLI_IMAGE_EDIT_PROVIDER", envOr("CHATCLI_IMAGE_EDIT_PROVIDER"))
	kv(p, "CHATCLI_IMAGE_EDIT_MODEL", envOr("CHATCLI_IMAGE_EDIT_MODEL"))

	fmt.Println(p)
	fmt.Println(p + colorize(i18n.T("cfg.image.about"), ColorGray))
	fmt.Println(p + colorize(i18n.T("cfg.image.change_hint"), ColorGray))
	sectionEnd(ColorBlue)
}

func (cli *ChatCLI) printConfigImageUsage() {
	fmt.Println(colorize("  "+i18n.T("cfg.image.usage"), ColorGray))
}

// getConfigImageSuggestions powers `/config image …` autocompletion:
// subcommands, then per-subcommand values (provider names, api modes, model
// catalog).
func (cli *ChatCLI) getConfigImageSuggestions(d prompt.Document) []prompt.Suggest {
	line := d.TextBeforeCursor()
	args := strings.Fields(line)
	word := d.GetWordBeforeCursor()

	// Subcommand slot: `/config image <TAB>`
	if len(args) == 2 || (len(args) == 3 && !strings.HasSuffix(line, " ")) {
		return prompt.FilterHasPrefix([]prompt.Suggest{
			{Text: "provider", Description: i18n.T("complete.configimage.provider")},
			{Text: "api", Description: i18n.T("complete.configimage.api")},
			{Text: "model", Description: i18n.T("complete.configimage.model")},
			{Text: "url", Description: i18n.T("complete.configimage.url")},
			{Text: "edit", Description: i18n.T("complete.configimage.edit")},
			{Text: "models", Description: i18n.T("complete.configimage.models")},
			{Text: "reset", Description: i18n.T("complete.configimage.reset")},
		}, word, true)
	}

	// Value slot: `/config image <sub> <TAB>`
	if len(args) == 3 || (len(args) == 4 && !strings.HasSuffix(line, " ")) {
		switch strings.ToLower(args[2]) {
		case "provider":
			return prompt.FilterHasPrefix([]prompt.Suggest{
				{Text: "auto", Description: i18n.T("complete.configimage.val")},
				{Text: "sdwebui", Description: i18n.T("complete.configimage.val")},
				{Text: "openai", Description: i18n.T("complete.configimage.val")},
				{Text: "responses", Description: i18n.T("complete.configimage.val")},
				{Text: "google", Description: i18n.T("complete.configimage.val")},
				{Text: "xai", Description: i18n.T("complete.configimage.val")},
				{Text: "zai", Description: i18n.T("complete.configimage.val")},
				{Text: "minimax", Description: i18n.T("complete.configimage.val")},
				{Text: "bedrock", Description: i18n.T("complete.configimage.val")},
				{Text: "url", Description: i18n.T("complete.configimage.val")},
			}, word, true)
		case "edit":
			out := make([]prompt.Suggest, 0)
			for _, b := range imagegen.Backends() {
				out = append(out, prompt.Suggest{Text: b.Name, Description: i18n.T("complete.configimage.val")})
			}
			return prompt.FilterHasPrefix(out, word, true)
		case "api":
			return prompt.FilterHasPrefix([]prompt.Suggest{
				{Text: "images", Description: i18n.T("complete.configimage.val")},
				{Text: "responses", Description: i18n.T("complete.configimage.val")},
			}, word, true)
		case "model":
			out := make([]prompt.Suggest, 0)
			for _, m := range imagegen.KnownModels() {
				out = append(out, prompt.Suggest{Text: m.Name, Description: m.Provider + " · " + m.API})
			}
			return prompt.FilterHasPrefix(out, word, true)
		}
	}
	return nil
}

// imageModelsCatalog renders the models per backend: the catalog merged
// with each configured provider's live listing (same view as @image models).
func (cli *ChatCLI) imageModelsCatalog(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	active := imagegen.ActiveBackend(imagegen.NewFromEnvContext(ctx, cli.logger))
	return plugins.RenderImageModels(imagegen.ListModels(ctx, "", active, true, cli.logger))
}
