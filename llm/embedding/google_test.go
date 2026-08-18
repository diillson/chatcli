/*
 * ChatCLI - Google Gemini embeddings tests.
 *
 * Network-free: a local httptest server plays the :batchEmbedContents
 * endpoint, pinning the request shape (models/ prefix, parts, the
 * outputDimensionality knob) and the chunking + normalization behavior.
 */
package embedding

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewGoogle_Defaults(t *testing.T) {
	if _, err := NewGoogle("", "", 0); err == nil {
		t.Fatal("expected error for empty API key")
	}
	p, err := NewGoogle("k", "", 0)
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}
	if p.model != googleDefaultModel {
		t.Errorf("default model = %q, want %q", p.model, googleDefaultModel)
	}
	if p.Dimension() != googleDefaultDim {
		t.Errorf("default dim = %d, want %d", p.Dimension(), googleDefaultDim)
	}
	if p.Name() != "google:"+googleDefaultModel {
		t.Errorf("name = %q", p.Name())
	}
}

func TestGoogleEmbed_BatchShapeAndChunking(t *testing.T) {
	var calls int
	var lastPath, lastKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		lastPath = r.URL.Path
		lastKey = r.Header.Get("x-goog-api-key")
		var body googleBatchRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		for _, req := range body.Requests {
			if !strings.HasPrefix(req.Model, "models/") || len(req.Content.Parts) != 1 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			// Native dim requested — the truncation knob must be absent.
			if req.OutputDimensionality != 0 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		}
		var resp googleBatchResponse
		resp.Embeddings = make([]struct {
			Values []float32 `json:"values"`
		}, len(body.Requests))
		for i := range resp.Embeddings {
			resp.Embeddings[i].Values = []float32{1, 0}
		}
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p, err := NewGoogle("test-key", "", 0)
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}
	p.endpoint = srv.URL
	p.batchMax = 2 // force chunking with a small batch

	texts := []string{"a", "b", "c", "d", "e"}
	vecs, err := p.Embed(context.Background(), texts)
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	if len(vecs) != len(texts) {
		t.Fatalf("got %d vectors for %d inputs", len(vecs), len(texts))
	}
	if calls != 3 {
		t.Errorf("expected 3 chunked calls for 5 texts at batchMax=2; got %d", calls)
	}
	if !strings.Contains(lastPath, googleDefaultModel+":batchEmbedContents") {
		t.Errorf("unexpected path %q", lastPath)
	}
	if lastKey != "test-key" {
		t.Errorf("API key header missing; got %q", lastKey)
	}
}

func TestGoogleEmbed_TruncatedDimNormalizes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body googleBatchRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if len(body.Requests) != 1 || body.Requests[0].OutputDimensionality != 2 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		// Unnormalized on purpose — the provider must L2-normalize.
		_, _ = fmt.Fprint(w, `{"embeddings":[{"values":[3,4]}]}`)
	}))
	defer srv.Close()

	p, err := NewGoogle("k", "gemini-embedding-001", 2)
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}
	p.endpoint = srv.URL
	vecs, err := p.Embed(context.Background(), []string{"x"})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	norm := math.Sqrt(float64(vecs[0][0])*float64(vecs[0][0]) + float64(vecs[0][1])*float64(vecs[0][1]))
	if math.Abs(norm-1) > 1e-6 {
		t.Errorf("vector not unit-length after normalization: %v (norm %f)", vecs[0], norm)
	}
}

func TestGoogleEmbed_ErrorSurface(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `{"error":{"message":"key invalid"}}`)
	}))
	defer srv.Close()
	p, err := NewGoogle("bad", "", 0)
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}
	p.endpoint = srv.URL
	if _, err := p.Embed(context.Background(), []string{"x"}); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("expected HTTP 403 error; got %v", err)
	}
}

func TestNewByName_Google(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "gk")
	t.Setenv("GOOGLEAI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("CHATCLI_EMBED_MODEL", "")
	t.Setenv("CHATCLI_EMBED_DIMENSIONS", "1536")
	p, err := NewByName("google")
	if err != nil {
		t.Fatalf("google factory: %v", err)
	}
	gp, ok := p.(*Google)
	if !ok {
		t.Fatalf("expected *Google; got %T", p)
	}
	if gp.model != googleDefaultModel || gp.Dimension() != 1536 {
		t.Errorf("model=%q dim=%d; want %q/1536", gp.model, gp.Dimension(), googleDefaultModel)
	}
	if _, err := NewByName("gemini"); err != nil {
		t.Errorf("gemini alias must resolve: %v", err)
	}
}
