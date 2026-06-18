/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Package visionwire converts the provider-agnostic models.ImageContent
// carried on a message into the per-provider wire formats for vision
// (multimodal) input. Centralizing the dialect knowledge here keeps each
// provider adapter to a one-line change and guarantees the text-only path
// stays byte-identical: every builder marshals to the plain string content
// when a message has no (valid) images, so caching and existing behavior are
// untouched for non-vision turns.
package visionwire

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/diillson/chatcli/models"
)

// Content is the polymorphic content value of a chat message. On the wire a
// turn is either a bare string (text-only) or an ordered list of typed parts
// (a multimodal/vision turn); Content models exactly that and marshals to the
// shape each provider expects. Returning this concrete type — instead of
// interface{} — keeps the content strongly typed at every call site while the
// dialect-specific parts stay an implementation detail.
type Content struct {
	text  string
	parts []any // dialect-specific part objects; nil ⇒ marshal as a string
	array bool  // force the array form even when parts is empty/text-only
}

// MarshalJSON renders the string form for a text-only turn and the parts array
// otherwise, so a Content dropped into a provider's request map serializes to
// the exact bytes the legacy code produced.
func (c Content) MarshalJSON() ([]byte, error) {
	if c.array || c.parts != nil {
		return json.Marshal(c.parts)
	}
	return json.Marshal(c.text)
}

// textContent is the text-only form (marshals as a JSON string).
func textContent(s string) Content { return Content{text: s} }

// validImages filters to images that carry usable bytes/URL and a supported
// media type, so a malformed attachment never reaches the wire.
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
// OpenAI-compatible image_url parts. When the image is URL-only, the remote
// URL is returned as-is (those providers fetch it themselves).
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
// Completions message. With no valid images it is the plain text string
// (byte-identical to the legacy path). With images it is the multimodal parts
// array: a text part (when text is non-empty) followed by one image_url part
// per image. Shared by openai, xai, zai, openrouter, copilot, githubmodels,
// moonshot, minimax, ollama.
func OpenAIContent(text string, imgs []models.ImageContent) Content {
	valid := validImages(imgs)
	if len(valid) == 0 {
		return textContent(text)
	}
	parts := make([]any, 0, len(valid)+1)
	if strings.TrimSpace(text) != "" {
		parts = append(parts, map[string]interface{}{"type": "text", "text": text})
	}
	for _, ic := range valid {
		parts = append(parts, map[string]interface{}{
			"type":      "image_url",
			"image_url": map[string]interface{}{"url": DataURL(ic)},
		})
	}
	return Content{parts: parts, array: true}
}

// AnthropicContent builds the `content` value for an Anthropic Messages API
// user/assistant turn. With no valid images it is the plain string. With images
// it is a blocks array with the images first (Anthropic's recommended ordering
// for best grounding) followed by a text block.
func AnthropicContent(text string, imgs []models.ImageContent) Content {
	valid := validImages(imgs)
	if len(valid) == 0 {
		return textContent(text)
	}
	return Content{parts: anthropicBlocks(text, valid), array: true}
}

// AnthropicOAuthContent is the OAuth (claude.ai) flavor, which always wraps a
// turn's content in a blocks array — even text-only. It produces
// [image blocks..., {type:text,text}], matching the hand-built oauthTextBlock
// shape so the wire bytes are unchanged for text-only turns.
func AnthropicOAuthContent(text string, imgs []models.ImageContent) Content {
	return Content{parts: anthropicBlocks(text, validImages(imgs)), array: true}
}

// anthropicBlocks builds the image-first, text-last blocks slice shared by the
// API-key (when images present) and OAuth (always) Anthropic paths.
func anthropicBlocks(text string, valid []models.ImageContent) []any {
	blocks := make([]any, 0, len(valid)+1)
	for _, ic := range valid {
		blocks = append(blocks, anthropicImageBlock(ic))
	}
	blocks = append(blocks, map[string]interface{}{"type": "text", "text": text})
	return blocks
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

// ResponsesUserContent builds the `content` value for an OpenAI Responses API
// user message. With no valid images it is the plain string. With images it is
// the parts array using the Responses item types (input_text + input_image,
// where image_url is a data URL string).
func ResponsesUserContent(text string, imgs []models.ImageContent) Content {
	valid := validImages(imgs)
	if len(valid) == 0 {
		return textContent(text)
	}
	parts := make([]any, 0, len(valid)+1)
	if strings.TrimSpace(text) != "" {
		parts = append(parts, map[string]interface{}{"type": "input_text", "text": text})
	}
	for _, ic := range valid {
		parts = append(parts, map[string]interface{}{
			"type":      "input_image",
			"image_url": DataURL(ic),
		})
	}
	return Content{parts: parts, array: true}
}

// GeminiParts builds the `parts` array value for a Gemini content entry. With
// no valid images it is a single text part (byte-identical to the legacy
// single-text-part path). With images it appends an inline_data part per image.
// Gemini requires inline bytes, so URL-only images are skipped.
func GeminiParts(text string, imgs []models.ImageContent) Content {
	valid := validImages(imgs)
	parts := make([]any, 0, len(valid)+1)
	if strings.TrimSpace(text) != "" || len(valid) == 0 {
		parts = append(parts, map[string]interface{}{"text": text})
	}
	for _, ic := range valid {
		if len(ic.Data) == 0 {
			continue
		}
		mt, _ := models.NormalizeImageMediaType(ic.MediaType)
		if mt == "" {
			mt = "image/png"
		}
		parts = append(parts, map[string]interface{}{
			"inline_data": map[string]interface{}{
				"mime_type": mt,
				"data":      base64.StdEncoding.EncodeToString(ic.Data),
			},
		})
	}
	return Content{parts: parts, array: true}
}
