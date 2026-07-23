package githubmodels

import (
	"context"
	"errors"
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
	var apiErr *utils.APIError
	if !errors.As(err, &apiErr) {
		t.Errorf("expected *utils.APIError, got %T", err)
	}
}

// The third GetMaxTokens argument is a highest-priority OVERRIDE, not a
// fallback — passing 4096 there bypassed the catalog entirely and strangled
// every model (gpt-4o's real 16K ceiling included) down to 4096.
func TestGitHubModelsClient_GetMaxTokensUsesCatalog(t *testing.T) {
	logger := zap.NewNop()
	c := NewGitHubModelsClient(testProvider("t"), "gpt-4o", logger, 1, 0)
	if got := c.getMaxTokens(); got != 16384 {
		t.Fatalf("gpt-4o must use its catalog ceiling of 16384, got %d", got)
	}
	unknown := NewGitHubModelsClient(testProvider("t"), "some-unknown-model", logger, 1, 0)
	if got := unknown.getMaxTokens(); got != 4096 {
		t.Fatalf("unknown models keep the provider fallback of 4096, got %d", got)
	}
	t.Setenv("GITHUB_MODELS_MAX_TOKENS", "2222")
	if got := c.getMaxTokens(); got != 2222 {
		t.Fatalf("env override keeps top precedence, got %d", got)
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
