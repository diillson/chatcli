/*
 * ChatCLI - Bedrock embeddings tests.
 *
 * Network-free: every test exercises pure helpers (family resolution,
 * dim validation, body schema). The runtime client is built lazily
 * on first Embed, so NewBedrock returning a valid provider does not
 * touch AWS — exactly the behavior we want for /config dispatch.
 */
package embedding

import (
	"encoding/json"
	"testing"
)

func TestResolveEmbedFamily(t *testing.T) {
	cases := map[string]embedFamily{
		"amazon.titan-embed-text-v2:0":  embedFamilyTitan,
		"amazon.titan-embed-text-v1":    embedFamilyTitan,
		"amazon.titan-embed-image-v1":   embedFamilyTitan,
		"cohere.embed-english-v3":       embedFamilyCohere,
		"cohere.embed-multilingual-v3":  embedFamilyCohere,
		"us.amazon.titan-embed-text-v2": embedFamilyTitan,
		"":                              embedFamilyTitan, // default safety
	}
	for id, want := range cases {
		if got := resolveEmbedFamily(id); got != want {
			t.Errorf("resolveEmbedFamily(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestDefaultDim(t *testing.T) {
	cases := []struct {
		model string
		want  int
	}{
		{"amazon.titan-embed-text-v2:0", titanV2DefaultDim},
		{"amazon.titan-embed-text-v1", titanV1Dim},
		{"cohere.embed-english-v3", cohereV3Dim},
	}
	for _, tc := range cases {
		family := resolveEmbedFamily(tc.model)
		if got := defaultDim(tc.model, family); got != tc.want {
			t.Errorf("defaultDim(%q) = %d, want %d", tc.model, got, tc.want)
		}
	}
}

func TestIsValidTitanDim(t *testing.T) {
	cases := []struct {
		model string
		dim   int
		want  bool
	}{
		// Titan v2 — only 256/512/1024 are valid.
		{"amazon.titan-embed-text-v2:0", 256, true},
		{"amazon.titan-embed-text-v2:0", 512, true},
		{"amazon.titan-embed-text-v2:0", 1024, true},
		{"amazon.titan-embed-text-v2:0", 768, false},
		{"amazon.titan-embed-text-v2:0", 1536, false},
		{"amazon.titan-embed-text-v2:0", 0, false},
		// Titan v1 — fixed at 1536.
		{"amazon.titan-embed-text-v1", 1536, true},
		{"amazon.titan-embed-text-v1", 1024, false},
	}
	for _, tc := range cases {
		if got := isValidTitanDim(tc.model, tc.dim); got != tc.want {
			t.Errorf("isValidTitanDim(%q, %d) = %v, want %v", tc.model, tc.dim, got, tc.want)
		}
	}
}

func TestNewBedrock_DefaultsAndOverrides(t *testing.T) {
	p, err := NewBedrock("", "us-east-1", "", 0, nil)
	if err != nil {
		t.Fatalf("default constructor must not error: %v", err)
	}
	if p.model != bedrockDefaultModel {
		t.Errorf("default model = %q, want %q", p.model, bedrockDefaultModel)
	}
	if p.Dimension() != titanV2DefaultDim {
		t.Errorf("default dim = %d, want %d", p.Dimension(), titanV2DefaultDim)
	}
	if p.Name() != "bedrock:"+bedrockDefaultModel {
		t.Errorf("name = %q", p.Name())
	}
}

func TestNewBedrock_RejectsInvalidTitanDim(t *testing.T) {
	if _, err := NewBedrock("amazon.titan-embed-text-v2:0", "us-east-1", "", 999, nil); err == nil {
		t.Fatal("expected error for invalid Titan v2 dimension")
	}
}

func TestNewBedrock_AcceptsCohereDim(t *testing.T) {
	p, err := NewBedrock("cohere.embed-english-v3", "us-east-1", "", 0, nil)
	if err != nil {
		t.Fatalf("cohere constructor: %v", err)
	}
	if p.family != embedFamilyCohere {
		t.Errorf("expected cohere family; got %q", p.family)
	}
	if p.Dimension() != cohereV3Dim {
		t.Errorf("cohere dim = %d, want %d", p.Dimension(), cohereV3Dim)
	}
}

// TestTitanRequestShape pins the JSON body Bedrock expects for Titan v2:
// inputText + dimensions + normalize. AWS rejects unknown fields here,
// so silent shape drift would break embeddings without us noticing.
func TestTitanRequestShape(t *testing.T) {
	body := titanRequest{InputText: "hello", Dimensions: 1024, Normalize: true}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["inputText"] != "hello" {
		t.Errorf("inputText missing or wrong: %v", got)
	}
	if got["dimensions"].(float64) != 1024 {
		t.Errorf("dimensions missing or wrong: %v", got["dimensions"])
	}
	if got["normalize"] != true {
		t.Errorf("normalize missing or wrong: %v", got["normalize"])
	}
}

func TestCohereRequestShape(t *testing.T) {
	body := cohereRequest{
		Texts:     []string{"a", "b"},
		InputType: "search_document",
		Truncate:  "END",
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if texts, ok := got["texts"].([]interface{}); !ok || len(texts) != 2 {
		t.Errorf("texts missing or wrong: %v", got["texts"])
	}
	if got["input_type"] != "search_document" {
		t.Errorf("input_type missing or wrong: %v", got["input_type"])
	}
}

// TestTitanResponseDecode pins the parser against the canonical Titan v2
// response shape. Drift here yields zero-length embeddings silently.
func TestTitanResponseDecode(t *testing.T) {
	raw := []byte(`{"embedding":[0.1,0.2,0.3],"inputTextTokenCount":3}`)
	var parsed titanResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(parsed.Embedding) != 3 {
		t.Errorf("expected 3-dim embedding; got %d", len(parsed.Embedding))
	}
	if parsed.InputTextTokenCount != 3 {
		t.Errorf("token count = %d", parsed.InputTextTokenCount)
	}
}

func TestCohereResponseDecode(t *testing.T) {
	raw := []byte(`{"embeddings":[[0.1,0.2],[0.3,0.4]]}`)
	var parsed cohereResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(parsed.Embeddings) != 2 {
		t.Errorf("expected 2 vectors; got %d", len(parsed.Embeddings))
	}
}

func TestNewByName_Bedrock(t *testing.T) {
	t.Setenv("BEDROCK_REGION", "us-east-1")
	t.Setenv("CHATCLI_EMBED_MODEL", "")
	t.Setenv("CHATCLI_EMBED_DIMENSIONS", "")
	p, err := NewByName("bedrock")
	if err != nil {
		t.Fatalf("bedrock factory: %v", err)
	}
	if IsNull(p) {
		t.Fatal("bedrock provider must not be null")
	}
	bp, ok := p.(*Bedrock)
	if !ok {
		t.Fatalf("expected *Bedrock; got %T", p)
	}
	if bp.model != bedrockDefaultModel {
		t.Errorf("default model = %q, want %q", bp.model, bedrockDefaultModel)
	}
}

func TestNewByName_BedrockWithCustomDim(t *testing.T) {
	t.Setenv("BEDROCK_REGION", "us-east-1")
	t.Setenv("CHATCLI_EMBED_MODEL", "amazon.titan-embed-text-v2:0")
	t.Setenv("CHATCLI_EMBED_DIMENSIONS", "512")
	p, err := NewByName("bedrock")
	if err != nil {
		t.Fatalf("bedrock factory: %v", err)
	}
	if got := p.Dimension(); got != 512 {
		t.Errorf("dim = %d, want 512", got)
	}
}
