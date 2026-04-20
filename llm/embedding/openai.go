/*
 * ChatCLI - OpenAI embeddings provider.
 *
 * Defaults to text-embedding-3-small (1536 dim, $0.02/1M tokens).
 * The dimensions field (override via CHATCLI_EMBED_DIMENSIONS) lets
 * users opt into smaller vectors for cheaper storage when the
 * downstream cosine-similarity quality is acceptable.
 */
package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	openaiDefaultModel = "text-embedding-3-small"
	openaiDefaultDim   = 1536
	openaiEndpoint     = "https://api.openai.com/v1/embeddings"
)

// OpenAI is the OpenAI embeddings provider.
type OpenAI struct {
	apiKey   string
	model    string
	endpoint string
	dim      int
	client   *http.Client
}

// NewOpenAI constructs the provider. When dim <= 0 the model default
// dimension is used. apiKey is required.
func NewOpenAI(apiKey, model string, dim int) (*OpenAI, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("openai embeddings: API key is required (set OPENAI_API_KEY)")
	}
	if strings.TrimSpace(model) == "" {
		model = openaiDefaultModel
	}
	if dim <= 0 {
		dim = openaiDefaultDim
	}
	return &OpenAI{
		apiKey:   apiKey,
		model:    model,
		endpoint: openaiEndpoint,
		dim:      dim,
		client:   &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// Name identifies the provider.
func (o *OpenAI) Name() string { return "openai:" + o.model }

// Dimension returns the requested vector dim.
func (o *OpenAI) Dimension() int { return o.dim }

type openaiRequest struct {
	Input          []string `json:"input"`
	Model          string   `json:"model"`
	Dimensions     int      `json:"dimensions,omitempty"`
	EncodingFormat string   `json:"encoding_format,omitempty"`
}

type openaiResponseDatum struct {
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

type openaiResponse struct {
	Data  []openaiResponseDatum `json:"data"`
	Model string                `json:"model"`
}

// Embed converts texts to vectors via the OpenAI embeddings API.
func (o *OpenAI) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	body := openaiRequest{
		Input:          texts,
		Model:          o.model,
		EncodingFormat: "float",
	}
	if o.dim != openaiDefaultDim {
		body.Dimensions = o.dim
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("openai embeddings marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+o.apiKey)

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai embeddings request: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openai embeddings HTTP %d: %s", resp.StatusCode, truncateBytes(raw, 200))
	}
	var parsed openaiResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("openai embeddings decode: %w", err)
	}
	if len(parsed.Data) != len(texts) {
		return nil, fmt.Errorf("openai returned %d vectors for %d inputs", len(parsed.Data), len(texts))
	}
	out := make([][]float32, len(texts))
	for _, d := range parsed.Data {
		if d.Index < 0 || d.Index >= len(out) {
			return nil, fmt.Errorf("openai out-of-range index %d", d.Index)
		}
		out[d.Index] = d.Embedding
	}
	return out, nil
}
