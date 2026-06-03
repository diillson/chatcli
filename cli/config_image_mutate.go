/*
 * ChatCLI - /config image mutator.
 * Copyright (c) 2024 Edilson Freitas
 *
 * Exposes the @image backend on the /config surface — read-only panorama plus
 * runtime mutation. The imagegen factory reads os.Getenv on every call, so each
 * setter takes effect immediately; a hint points to .env for a permanent
 * default (we never rewrite .env — user territory).
 *
 *   /config image                      # status
 *   /config image provider <name>      # sdwebui|url|openai|responses|google|xai|auto
 *   /config image api images|responses # OpenAI: Images API vs Responses API
 *   /config image model <id>           # CHATCLI_IMAGE_MODEL
 *   /config image url <url>            # CHATCLI_IMAGE_URL (self-hosted/SD WebUI)
 *   /config image models               # list the image-model catalog
 *   /config image reset                # clear the overrides above
 */
package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/imagegen"
)

// routeConfigImage dispatches /config image <sub> [args].
func (cli *ChatCLI) routeConfigImage(args []string) {
	if len(args) == 0 {
		cli.showConfigImage()
		return
	}
	switch strings.ToLower(args[0]) {
	case "help", "-h", "--help":
		cli.printConfigImageUsage()
	case "provider":
		cli.setImageEnv("CHATCLI_IMAGE_PROVIDER", args[1:])
	case "api":
		cli.setImageEnv("CHATCLI_IMAGE_API", args[1:])
	case "model":
		cli.setImageEnv("CHATCLI_IMAGE_MODEL", args[1:])
	case "url":
		cli.setImageEnv("CHATCLI_IMAGE_URL", args[1:])
	case "models", "catalog":
		fmt.Println(cli.imageModelsCatalog())
	case "reset":
		for _, k := range []string{"CHATCLI_IMAGE_PROVIDER", "CHATCLI_IMAGE_API", "CHATCLI_IMAGE_MODEL", "CHATCLI_IMAGE_URL"} {
			_ = os.Unsetenv(k)
		}
		fmt.Println(colorize("  "+i18n.T("cfg.image.reset_ok"), ColorGreen))
		cli.showConfigImage()
	default:
		fmt.Println(colorize("  "+i18n.T("cfg.image.unknown_sub", args[0]), ColorYellow))
		cli.printConfigImageUsage()
	}
}

// setImageEnv applies a single image env override at runtime.
func (cli *ChatCLI) setImageEnv(key string, args []string) {
	if len(args) == 0 {
		fmt.Println(colorize("  "+i18n.T("cfg.image.value_required", key), ColorYellow))
		return
	}
	val := strings.TrimSpace(strings.Join(args, " "))
	_ = os.Setenv(key, val)
	fmt.Println(colorize("  "+i18n.T("cfg.image.set_ok", key, val), ColorGreen))
	cli.showConfigImage()
}

// showConfigImage renders the @image panorama.
func (cli *ChatCLI) showConfigImage() {
	sectionHeader("🎨", "cfg.section.image.title", ColorBlue)
	p := uiPrefix(ColorBlue)

	status := i18n.T("cfg.val.imagegen_off")
	if g := imagegen.NewFromEnv(cli.logger); !imagegen.IsNull(g) {
		status = g.Name()
	}
	kv(p, i18n.T("cfg.kv.imagegen"), status)
	kv(p, "CHATCLI_IMAGE_PROVIDER", envOr("CHATCLI_IMAGE_PROVIDER"))
	kv(p, "CHATCLI_IMAGE_API", envOr("CHATCLI_IMAGE_API"))
	kv(p, "CHATCLI_IMAGE_MODEL", envOr("CHATCLI_IMAGE_MODEL"))
	kv(p, "CHATCLI_IMAGE_URL", envOr("CHATCLI_IMAGE_URL"))

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
				{Text: "bedrock", Description: i18n.T("complete.configimage.val")},
				{Text: "url", Description: i18n.T("complete.configimage.val")},
			}, word, true)
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

// imageModelsCatalog renders the static image-model catalog plus, when an
// OpenAI key is present, the image-capable models on the account.
func (cli *ChatCLI) imageModelsCatalog() string {
	var b strings.Builder
	b.WriteString(i18n.T("cfg.image.catalog_header"))
	b.WriteByte('\n')
	for _, m := range imagegen.KnownModels() {
		fmt.Fprintf(&b, "  • %-26s %-8s %-10s %s\n", m.Name, m.Provider, m.API, m.Note)
	}
	if key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); key != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if ids, err := imagegen.FetchOpenAIModels(ctx, "", key, cli.logger); err == nil && len(ids) > 0 {
			b.WriteString("\n" + i18n.T("cfg.image.catalog_live") + "\n  " + strings.Join(ids, ", "))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
