/*
 * ChatCLI - Google Gemini embeddings provider.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Defaults to gemini-embedding-2 (3072 dim, multimodal-ready, GA as of
 * Aug 2026 — gemini-embedding-001 is superseded and shuts down
 * 2028-05-14). Batches ride the :batchEmbedContents endpoint. The
 * output_dimensionality knob (CHATCLI_EMBED_DIMENSIONS, 128–3072 via
 * MRL/Matryoshka) trades quality for storage; truncated vectors are
 * L2-normalized locally because gemini-embedding-001 returns them
 * unnormalized (gemini-embedding-2 auto-normalizes — renormalizing is
 * a no-op there).
 */
package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

const (
	googleDefaultModel = "gemini-embedding-2"
	googleDefaultDim   = 3072
	googleEndpointBase = "https://generativelanguage.googleapis.com/v1beta"
	// googleBatchMax is the request cap of :batchEmbedContents — larger
	// batches are chunked into sequential calls.
	googleBatchMax = 100
)

// Google is the Gemini API embeddings provider.
type Google struct {
	apiKey   string
	model    string
	endpoint string
	dim      int
	batchMax int
	client   *http.Client
}

// NewGoogle constructs the provider. apiKey is required. When dim <= 0
// the model's native dimension (3072) is used and no truncation is
// requested from the API.
func NewGoogle(apiKey, model string, dim int) (*Google, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("google embeddings: API key is required (set GEMINI_API_KEY or GOOGLEAI_API_KEY)")
	}
	if strings.TrimSpace(model) == "" {
		model = googleDefaultModel
	}
	if dim <= 0 {
		dim = googleDefaultDim
	}
	return &Google{
		apiKey:   apiKey,
		model:    model,
		endpoint: googleEndpointBase,
		dim:      dim,
		batchMax: googleBatchMax,
		client:   &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// Name identifies the provider in /config quality output.
func (g *Google) Name() string { return "google:" + g.model }

// Dimension returns the vector dimensionality.
func (g *Google) Dimension() int { return g.dim }

type googleEmbedPart struct {
	Text string `json:"text"`
}

type googleEmbedContent struct {
	Parts []googleEmbedPart `json:"parts"`
}

type googleEmbedRequest struct {
	Model                string             `json:"model"`
	Content              googleEmbedContent `json:"content"`
	OutputDimensionality int                `json:"outputDimensionality,omitempty"`
}

type googleBatchRequest struct {
	Requests []googleEmbedRequest `json:"requests"`
}

type googleBatchResponse struct {
	Embeddings []struct {
		Values []float32 `json:"values"`
	} `json:"embeddings"`
}

// Embed sends the batch through :batchEmbedContents (chunked at the
// API's request cap) and returns the vectors in input order.
func (g *Google) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += g.batchMax {
		end := start + g.batchMax
		if end > len(texts) {
			end = len(texts)
		}
		vecs, err := g.embedChunk(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	return out, nil
}

func (g *Google) embedChunk(ctx context.Context, texts []string) ([][]float32, error) {
	reqs := make([]googleEmbedRequest, len(texts))
	for i, text := range texts {
		reqs[i] = googleEmbedRequest{
			Model:   "models/" + g.model,
			Content: googleEmbedContent{Parts: []googleEmbedPart{{Text: text}}},
		}
		if g.dim != googleDefaultDim {
			reqs[i].OutputDimensionality = g.dim
		}
	}
	payload, err := json.Marshal(googleBatchRequest{Requests: reqs})
	if err != nil {
		return nil, fmt.Errorf("google embeddings marshal: %w", err)
	}
	endpoint := fmt.Sprintf("%s/models/%s:batchEmbedContents", strings.TrimRight(g.endpoint, "/"), g.model)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", g.apiKey)

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("google embeddings request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("google embeddings HTTP %d: %s", resp.StatusCode, truncateBytes(body, 200))
	}
	var parsed googleBatchResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("google embeddings decode: %w", err)
	}
	if len(parsed.Embeddings) != len(texts) {
		return nil, fmt.Errorf("google embeddings returned %d vectors for %d inputs", len(parsed.Embeddings), len(texts))
	}
	out := make([][]float32, len(texts))
	for i, e := range parsed.Embeddings {
		if len(e.Values) == 0 {
			return nil, fmt.Errorf("google embeddings: empty vector at index %d", i)
		}
		out[i] = l2Normalize(e.Values)
	}
	return out, nil
}

// l2Normalize scales v to unit length in place and returns it. Cosine
// similarity is scale-invariant, so normalizing an already-normalized
// vector is a no-op — this only matters for truncated dims on
// gemini-embedding-001, which the API returns unnormalized.
func l2Normalize(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return v
	}
	norm := math.Sqrt(sum)
	for i := range v {
		v[i] = float32(float64(v[i]) / norm)
	}
	return v
}
