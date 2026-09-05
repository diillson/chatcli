package xai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func TestCountTokensReturnsTheTokenCount(t *testing.T) {
	var body map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/tokenize-text") {
			t.Errorf("path = %q, want the tokenizer endpoint", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-xai-key" {
			t.Errorf("Authorization = %q", got)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_, _ = w.Write([]byte(`{"token_ids":[1,2,3,4,5]}`))
	}))
	defer srv.Close()

	c := NewXAIClient(testProvider("test-xai-key"), "grok-4", zap.NewNop(), 1, 0)
	c.apiURL = srv.URL + "/v1/chat/completions"

	n, err := c.CountTokens(context.Background(), "and now?",
		[]models.Message{{Role: "system", Content: "be careful"}, {Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("CountTokens: %v", err)
	}
	if n != 5 {
		t.Errorf("count = %d, want the length of the token array", n)
	}
	// The whole conversation travels, roles included — they are billed too.
	text, _ := body["text"].(string)
	for _, want := range []string{"be careful", "hi", "and now?", "system:", "user:"} {
		if !strings.Contains(text, want) {
			t.Errorf("text is missing %q: %q", want, text)
		}
	}
}

// Both array spellings count: a rename would otherwise read as a
// zero-token prompt, which is worse than an error.
func TestCountTokensAcceptsEitherArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tokens":["a","b","c"]}`))
	}))
	defer srv.Close()
	c := NewXAIClient(testProvider("k"), "grok-4", zap.NewNop(), 1, 0)
	c.apiURL = srv.URL + "/v1/chat/completions"
	n, err := c.CountTokens(context.Background(), "hi", nil)
	if err != nil || n != 3 {
		t.Fatalf("count = %d, err = %v", n, err)
	}
}

func TestCountTokensSurfacesFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer srv.Close()
	c := NewXAIClient(testProvider("k"), "grok-4", zap.NewNop(), 1, 0)
	c.apiURL = srv.URL + "/v1/chat/completions"
	if _, err := c.CountTokens(context.Background(), "hi", nil); err == nil {
		t.Error("an HTTP failure must be reported, not counted as zero")
	}

	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer empty.Close()
	c.apiURL = empty.URL + "/v1/chat/completions"
	if _, err := c.CountTokens(context.Background(), "hi", nil); err == nil {
		t.Error("a response with no tokens must be an error, not zero")
	}
}

func TestCountTokensSkipsAnEmptyConversation(t *testing.T) {
	c := NewXAIClient(testProvider("k"), "grok-4", zap.NewNop(), 1, 0)
	c.apiURL = "http://127.0.0.1:0/v1/chat/completions" // never dialed
	if n, err := c.CountTokens(context.Background(), "  ", nil); err != nil || n != 0 {
		t.Errorf("an empty conversation costs nothing and calls nothing: %d, %v", n, err)
	}
}
