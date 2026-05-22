package openai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func TestOpenAIClient_SendPromptStream_HappyPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-stream-key" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"hello"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":" world"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	originalURL := os.Getenv("OPENAI_API_URL")
	t.Setenv("OPENAI_API_URL", server.URL)
	t.Cleanup(func() { _ = os.Setenv("OPENAI_API_URL", originalURL) })

	logger, _ := zap.NewDevelopment()
	c := NewOpenAIClient(
		auth.NewStaticTokenProvider("test-stream-key", auth.AuthModeAPIKey, auth.ProviderOpenAI),
		"gpt-4o", logger, 1, 0,
	)

	ch, err := c.SendPromptStream(context.Background(), "Hi",
		[]models.Message{{Role: "user", Content: "Hi"}}, 100)
	if err != nil {
		t.Fatalf("SendPromptStream: %v", err)
	}

	var combined string
	gotDone := false
	for chunk := range ch {
		combined += chunk.Text
		if chunk.Done {
			gotDone = true
		}
	}
	if combined != "hello world" {
		t.Errorf("text = %q, want 'hello world'", combined)
	}
	if !gotDone {
		t.Error("did not receive Done chunk")
	}
}

func TestOpenAIClient_SendPromptStream_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad request"}`))
	}))
	defer server.Close()

	originalURL := os.Getenv("OPENAI_API_URL")
	t.Setenv("OPENAI_API_URL", server.URL)
	t.Cleanup(func() { _ = os.Setenv("OPENAI_API_URL", originalURL) })

	logger, _ := zap.NewDevelopment()
	c := NewOpenAIClient(
		auth.NewStaticTokenProvider("k", auth.AuthModeAPIKey, auth.ProviderOpenAI),
		"gpt-4o", logger, 1, 0,
	)

	_, err := c.SendPromptStream(context.Background(), "Hi",
		[]models.Message{{Role: "user", Content: "Hi"}}, 100)
	if err == nil {
		t.Fatal("expected error for 400")
	}
}

func TestOpenAIClient_SupportsStreaming(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	c := NewOpenAIClient(
		auth.NewStaticTokenProvider("k", auth.AuthModeAPIKey, auth.ProviderOpenAI),
		"gpt-4o", logger, 1, 0,
	)
	if !c.SupportsStreaming() {
		t.Error("OpenAI client should support streaming")
	}
}

func TestOpenAIClient_OAuthRetry(t *testing.T) {
	// Streaming with OAuth: first call returns 401, second returns OK with
	// a new bearer (we don't actually validate the header — the inline retry
	// path is what we want to cover).
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	originalURL := os.Getenv("OPENAI_API_URL")
	t.Setenv("OPENAI_API_URL", server.URL)
	t.Cleanup(func() { _ = os.Setenv("OPENAI_API_URL", originalURL) })

	logger, _ := zap.NewDevelopment()
	c := NewOpenAIClient(
		auth.NewStaticTokenProviderFromResolved("oauth:dummy", auth.ProviderOpenAI),
		"gpt-4o", logger, 1, 0,
	)
	ch, err := c.SendPromptStream(context.Background(), "Hi",
		[]models.Message{{Role: "user", Content: "Hi"}}, 100)
	if err != nil {
		t.Fatalf("SendPromptStream: %v", err)
	}
	for range ch {
		// drain
	}
	if calls != 2 {
		t.Errorf("server calls = %d, want 2 (one retry after 401)", calls)
	}
}
