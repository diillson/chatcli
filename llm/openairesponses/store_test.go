package openairesponses

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// The endpoint retains responses by default and ChatCLI never reads them
// back — it resends the conversation every turn. Leaving the field unset
// paid the retention without taking the benefit, so both paths now say so.
func TestResponsesSendsStoreFalse(t *testing.T) {
	var body map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`))
	}))
	defer server.Close()

	t.Setenv("OPENAI_RESPONSES_API_URL", server.URL)
	c := NewOpenAIResponsesClient(
		auth.NewStaticTokenProvider("sk-test", auth.AuthModeAPIKey, auth.ProviderOpenAI),
		"gpt-5.6-terra", zap.NewNop(), 1, 0)

	if _, err := c.SendPrompt(context.Background(), "hi",
		[]models.Message{{Role: "user", Content: "hi"}}, 100); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	store, ok := body["store"]
	if !ok {
		t.Fatalf("store must be explicit, payload: %+v", body)
	}
	if store != false {
		t.Errorf("store = %v, want false", store)
	}
}
