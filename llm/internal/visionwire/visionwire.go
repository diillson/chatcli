// Package visionwire converts the provider-agnostic models.ImageContent
// carried on a message into the per-provider wire formats for vision
// (multimodal) input. Centralizing the dialect knowledge here keeps each
// provider adapter to a one-line change and guarantees the text-only path
// stays byte-identical: every helper returns the plain string content when
// a message has no (valid) images, so caching and existing behavior are
// untouched for non-vision turns.
package visionwire

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/diillson/chatcli/models"
)

// validImages filters to images that carry usable bytes/URL and a
// supported media type, so a malformed attachment never reaches the wire.
func validImages(imgs []models.ImageContent) []models.ImageContent {
	if len(imgs) == 0 {
		return nil
	}
	out := make([]models.ImageContent, 0, len(imgs))
	for _, ic := range imgs {
		if ic.IsValid() {
			out = append(out, ic)
		}
	}
	return out
}

// HasImages reports whether the slice contains at least one valid image.
func HasImages(imgs []models.ImageContent) bool {
	return len(validImages(imgs)) > 0
}

// DataURL renders an image as the `data:<mime>;base64,<...>` form used by
// OpenAI-compatible image_url parts. When the image is URL-only, the
// remote URL is returned as-is (those providers fetch it themselves).
func DataURL(ic models.ImageContent) string {
	if len(ic.Data) == 0 && strings.TrimSpace(ic.URL) != "" {
		return ic.URL
	}
	mt, _ := models.NormalizeImageMediaType(ic.MediaType)
	if mt == "" {
		mt = "image/png"
	}
	return fmt.Sprintf("data:%s;base64,%s", mt, base64.StdEncoding.EncodeToString(ic.Data))
}

// OpenAIContent builds the `content` value for an OpenAI-compatible Chat
// Completions message. With no valid images it returns the plain text
// string (byte-identical to the legacy path). With images it returns the
// multimodal parts array: a text part (when text is non-empty) followed by
// one image_url part per image. Shared by openai, xai, zai, openrouter,
// copilot, githubmodels, moonshot, minimax, ollama.
func OpenAIContent(text string, imgs []models.ImageContent) interface{} {
	valid := validImages(imgs)
	if len(valid) == 0 {
		return text
	}
	parts := make([]interface{}, 0, len(valid)+1)
	if strings.TrimSpace(text) != "" {
		parts = append(parts, map[string]interface{}{"type": "text", "text": text})
	}
	for _, ic := range valid {
		parts = append(parts, map[string]interface{}{
			"type":      "image_url",
			"image_url": map[string]interface{}{"url": DataURL(ic)},
		})
	}
	return parts
}

// AnthropicContent builds the `content` value for an Anthropic Messages API
// user/assistant turn. With no valid images it returns the plain string.
// With images it returns a blocks array with the images first (Anthropic's
// recommended ordering for best grounding) followed by a text block.
func AnthropicContent(text string, imgs []models.ImageContent) interface{} {
	valid := validImages(imgs)
	if len(valid) == 0 {
		return text
	}
	blocks := make([]interface{}, 0, len(valid)+1)
	for _, ic := range valid {
		blocks = append(blocks, anthropicImageBlock(ic))
	}
	if strings.TrimSpace(text) != "" {
		blocks = append(blocks, map[string]interface{}{"type": "text", "text": text})
	}
	return blocks
}

// AnthropicContentBlocks is the []interface{} flavor for the OAuth path,
// which already wraps every turn's content in a blocks array. The provided
// textBlock (e.g. oauthTextBlock(text)) is appended after the images so the
// caller keeps its own text-block construction. Returns nil-safe slices.
func AnthropicContentBlocks(textBlock interface{}, imgs []models.ImageContent) []interface{} {
	valid := validImages(imgs)
	blocks := make([]interface{}, 0, len(valid)+1)
	for _, ic := range valid {
		blocks = append(blocks, anthropicImageBlock(ic))
	}
	if textBlock != nil {
		blocks = append(blocks, textBlock)
	}
	return blocks
}

// ResponsesUserContent builds the `content` value for an OpenAI Responses
// API user message. With no valid images it returns the plain string. With
// images it returns the parts array using the Responses item types
// (input_text + input_image, where image_url is a data URL string).
func ResponsesUserContent(text string, imgs []models.ImageContent) interface{} {
	valid := validImages(imgs)
	if len(valid) == 0 {
		return text
	}
	parts := make([]interface{}, 0, len(valid)+1)
	if strings.TrimSpace(text) != "" {
		parts = append(parts, map[string]interface{}{"type": "input_text", "text": text})
	}
	for _, ic := range valid {
		parts = append(parts, map[string]interface{}{
			"type":      "input_image",
			"image_url": DataURL(ic),
		})
	}
	return parts
}

func anthropicImageBlock(ic models.ImageContent) map[string]interface{} {
	// Prefer inline base64; fall back to a URL source when bytes are absent.
	if len(ic.Data) == 0 && strings.TrimSpace(ic.URL) != "" {
		return map[string]interface{}{
			"type":   "image",
			"source": map[string]interface{}{"type": "url", "url": ic.URL},
		}
	}
	mt, _ := models.NormalizeImageMediaType(ic.MediaType)
	if mt == "" {
		mt = "image/png"
	}
	return map[string]interface{}{
		"type": "image",
		"source": map[string]interface{}{
			"type":       "base64",
			"media_type": mt,
			"data":       base64.StdEncoding.EncodeToString(ic.Data),
		},
	}
}

// GeminiParts builds the `parts` array for a Gemini content entry. With no
// valid images it returns a single text part (byte-identical to the legacy
// single-text-part path). With images it appends an inline_data part per
// image. Each part is a map[string]interface{} so it carries either "text"
// or "inline_data".
func GeminiParts(text string, imgs []models.ImageContent) []map[string]interface{} {
	valid := validImages(imgs)
	parts := make([]map[string]interface{}, 0, len(valid)+1)
	if strings.TrimSpace(text) != "" || len(valid) == 0 {
		parts = append(parts, map[string]interface{}{"text": text})
	}
	for _, ic := range valid {
		mt, _ := models.NormalizeImageMediaType(ic.MediaType)
		if mt == "" {
			mt = "image/png"
		}
		// Gemini requires inline bytes (base64); a URL-only image must be
		// fetched by the caller before reaching here, so skip if no bytes.
		if len(ic.Data) == 0 {
			continue
		}
		parts = append(parts, map[string]interface{}{
			"inline_data": map[string]interface{}{
				"mime_type": mt,
				"data":      base64.StdEncoding.EncodeToString(ic.Data),
			},
		})
	}
	return parts
}
