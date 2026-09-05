package minimax

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

func TestCountTokensReadsInputTokens(t *testing.T) {
	var body map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/responses/input_tokens") {
			t.Errorf("path = %q, want the counting endpoint", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_, _ = w.Write([]byte(`{"object":"response.input_tokens","input_tokens":1234}`))
	}))
	defer srv.Close()

	c := NewMiniMaxClient(testProvider("k"), "MiniMax-M2.7", zap.NewNop(), 1, 0)
	c.apiURL = srv.URL + "/v1/text/chatcompletion_v2"

	n, err := c.CountTokens(context.Background(), "and now?",
		[]models.Message{{Role: "system", Content: "be careful"}})
	if err != nil {
		t.Fatalf("CountTokens: %v", err)
	}
	if n != 1234 {
		t.Errorf("count = %d, want the reported input_tokens", n)
	}
	items, _ := body["input"].([]interface{})
	if len(items) != 2 {
		t.Fatalf("want the history plus the prompt, got %+v", body["input"])
	}
}

// MiniMax reports application errors inside base_resp with HTTP 200, so a
// failure would otherwise read as a valid count.
func TestCountTokensSurfacesBaseRespErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"base_resp":{"status_code":1004,"status_msg":"invalid api key"}}`))
	}))
	defer srv.Close()
	c := NewMiniMaxClient(testProvider("k"), "MiniMax-M2.7", zap.NewNop(), 1, 0)
	c.apiURL = srv.URL + "/v1/text/chatcompletion_v2"
	if _, err := c.CountTokens(context.Background(), "hi", nil); err == nil {
		t.Error("an application error must be reported, not counted")
	}
}

// The compatibility surface speaks another vendor's schema against another
// base URL; this endpoint is not part of that contract.
func TestCountTokensAbsentOnTheCompatibilitySurface(t *testing.T) {
	c := NewMiniMaxClient(testProvider("k"), "MiniMax-M2.7", zap.NewNop(), 1, 0)
	c.anthropicCompat = true
	if _, err := c.CountTokens(context.Background(), "hi", nil); err == nil {
		t.Error("the capability must report absent rather than guess")
	}
}

func TestCountTokensSkipsAnEmptyConversation(t *testing.T) {
	c := NewMiniMaxClient(testProvider("k"), "MiniMax-M2.7", zap.NewNop(), 1, 0)
	c.apiURL = "http://127.0.0.1:0/v1/text/chatcompletion_v2"
	if n, err := c.CountTokens(context.Background(), "   ", nil); err != nil || n != 0 {
		t.Errorf("an empty conversation costs nothing and calls nothing: %d, %v", n, err)
	}
}
