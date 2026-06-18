package visionwire

import (
	"encoding/json"
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

// marshalContent renders a Content exactly as it serializes inside a provider
// request map (via its MarshalJSON), so the assertions check real wire bytes.
func marshalContent(t *testing.T, c Content) string {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func TestOpenAIContentTextOnlyIsString(t *testing.T) {
	got := marshalContent(t, OpenAIContent("hello", nil))
	if got != `"hello"` {
		t.Fatalf("text-only must marshal as a JSON string, got %s", got)
	}
}

func TestOpenAIContentWithImage(t *testing.T) {
	got := marshalContent(t, OpenAIContent("what is this?", []models.ImageContent{img()}))
	var parts []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(got), &parts); err != nil {
		t.Fatalf("expected a parts array, got %s (%v)", got, err)
	}
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d: %s", len(parts), got)
	}
	if !strings.Contains(got, `"type":"image_url"`) || !strings.Contains(got, "data:image/png;base64,") {
		t.Fatalf("missing image_url data URL: %s", got)
	}
}

func TestAnthropicContentImageFirst(t *testing.T) {
	got := marshalContent(t, AnthropicContent("caption", []models.ImageContent{img()}))
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(got), &blocks); err != nil {
		t.Fatalf("expected blocks array: %s (%v)", got, err)
	}
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	if string(blocks[0]["type"]) != `"image"` {
		t.Fatalf("image block must come first, got %s", got)
	}
}

func TestAnthropicOAuthAlwaysArray(t *testing.T) {
	// Text-only OAuth content must still be an array (not a bare string).
	got := marshalContent(t, AnthropicOAuthContent("hi", nil))
	if !strings.HasPrefix(strings.TrimSpace(got), "[") {
		t.Fatalf("OAuth content must always be an array, got %s", got)
	}
	if !strings.Contains(got, `"type":"text"`) || !strings.Contains(got, `"text":"hi"`) {
		t.Fatalf("OAuth text block malformed: %s", got)
	}
}

func TestGeminiPartsInlineData(t *testing.T) {
	got := marshalContent(t, GeminiParts("hi", []models.ImageContent{img()}))
	if !strings.Contains(got, `"inline_data"`) || !strings.Contains(got, `"text":"hi"`) {
		t.Fatalf("expected text + inline_data parts: %s", got)
	}
}

func TestResponsesUserContentInputImage(t *testing.T) {
	got := marshalContent(t, ResponsesUserContent("q", []models.ImageContent{img()}))
	if !strings.Contains(got, `"type":"input_image"`) || !strings.Contains(got, `"type":"input_text"`) {
		t.Fatalf("expected input_text + input_image parts: %s", got)
	}
}

func TestInvalidImageDroppedKeepsString(t *testing.T) {
	bad := models.ImageContent{MediaType: "application/pdf", Data: []byte("x")}
	if HasImages([]models.ImageContent{bad}) {
		t.Fatalf("unsupported media type must be filtered")
	}
	got := marshalContent(t, OpenAIContent("t", []models.ImageContent{bad}))
	if got != `"t"` {
		t.Fatalf("with no valid images, content must marshal as a string, got %s", got)
	}
}
