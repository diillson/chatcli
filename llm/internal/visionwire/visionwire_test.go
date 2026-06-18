package visionwire

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
)

// a 1x1 PNG.
var pngBytes = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
	0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89, 0x00, 0x00, 0x00,
	0x0a, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49,
	0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
}

func img() models.ImageContent {
	return models.ImageContent{MediaType: "image/png", Data: pngBytes, FileName: "x.png"}
}

func TestOpenAIContentTextOnly(t *testing.T) {
	got := OpenAIContent("hello", nil)
	if s, ok := got.(string); !ok || s != "hello" {
		t.Fatalf("text-only must stay a plain string, got %#v", got)
	}
}

func TestOpenAIContentWithImage(t *testing.T) {
	got := OpenAIContent("what is this?", []models.ImageContent{img()})
	parts, ok := got.([]interface{})
	if !ok || len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %#v", got)
	}
	last := parts[1].(map[string]interface{})
	if last["type"] != "image_url" {
		t.Fatalf("expected image_url part, got %#v", last)
	}
	url := last["image_url"].(map[string]interface{})["url"].(string)
	if !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Fatalf("bad data url: %q", url)
	}
}

func TestAnthropicContentImageFirst(t *testing.T) {
	got := AnthropicContent("caption", []models.ImageContent{img()})
	blocks := got.([]interface{})
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	if blocks[0].(map[string]interface{})["type"] != "image" {
		t.Fatalf("image must come first, got %#v", blocks[0])
	}
}

func TestGeminiPartsInlineData(t *testing.T) {
	parts := GeminiParts("hi", []models.ImageContent{img()})
	if len(parts) != 2 {
		t.Fatalf("expected text + inline_data, got %d", len(parts))
	}
	if _, ok := parts[1]["inline_data"]; !ok {
		t.Fatalf("expected inline_data part, got %#v", parts[1])
	}
}

func TestInvalidImageDropped(t *testing.T) {
	bad := models.ImageContent{MediaType: "application/pdf", Data: []byte("x")}
	if HasImages([]models.ImageContent{bad}) {
		t.Fatalf("unsupported media type must be filtered")
	}
	got := OpenAIContent("t", []models.ImageContent{bad})
	if _, ok := got.(string); !ok {
		t.Fatalf("with no valid images, content must stay a string")
	}
}
