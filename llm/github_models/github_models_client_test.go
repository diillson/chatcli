package github_models

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

func testProvider(token string) auth.TokenProvider {
	return auth.NewStaticTokenProvider(token, auth.AuthModeToken, auth.ProviderGitHubModels)
}

func TestGitHubModelsClient_SendPrompt_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ghp-test" {
			t.Errorf("Authorization = %q, want Bearer ghp-test", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Hello from GitHub Models!"}}]}`))
	}))
	defer server.Close()

	t.Setenv("GITHUB_MODELS_API_URL", server.URL)
	logger, _ := zap.NewDevelopment()
	c := NewGitHubModelsClient(testProvider("ghp-test"), "gpt-4o", logger, 1, 0)

	resp, err := c.SendPrompt(context.Background(), "Hi",
		[]models.Message{{Role: "user", Content: "Hi"}}, 100)
	if err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	if resp != "Hello from GitHub Models!" {
		t.Errorf("response = %q", resp)
	}
}

func TestGitHubModelsClient_SendPrompt_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"forbidden"}`))
	}))
	defer server.Close()

	t.Setenv("GITHUB_MODELS_API_URL", server.URL)
	logger, _ := zap.NewDevelopment()
	c := NewGitHubModelsClient(testProvider("bad"), "gpt-4o", logger, 1, 0)

	_, err := c.SendPrompt(context.Background(), "Hi",
		[]models.Message{{Role: "user", Content: "Hi"}}, 100)
	if err == nil {
		t.Fatal("expected error for 401")
	}
	if _, ok := err.(*utils.APIError); !ok {
		t.Errorf("expected *utils.APIError, got %T", err)
	}
}

func TestGitHubModelsClient_GetModelName(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	c := NewGitHubModelsClient(testProvider("t"), "gpt-4o", logger, 1, 0)
	if c.GetModelName() == "" {
		t.Error("empty model name")
	}
}

func TestGitHubModelsClient_ListModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"id":"openai/gpt-4o","name":"gpt-4o","friendly_name":"GPT-4o","publisher":"OpenAI","task":"chat-completion"},
			{"id":"meta/llama","name":"llama","task":"embeddings"}
		]`))
	}))
	defer server.Close()

	t.Setenv("GITHUB_MODELS_API_URL", server.URL+"/chat/completions")
	logger, _ := zap.NewDevelopment()
	c := NewGitHubModelsClient(testProvider("ghp"), "gpt-4o", logger, 1, 0)

	list, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	// Only the chat-completion model should pass the filter.
	if len(list) != 1 {
		t.Errorf("got %d models, want 1 (chat-completion only)", len(list))
	}
}
