/*
 * ChatCLI - StackSpot usage schema contract
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package stackspotai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/diillson/chatcli/llm/token"
	"github.com/diillson/chatcli/models"
	"github.com/stretchr/testify/mock"
	"go.uber.org/zap"
)

// TestStackSpotUsageIsWholeInput: user+enrichment is the whole input; this
// API has no cache split to double-count.
func TestStackSpotUsageIsWholeInput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"message":"hi","tokens":{"user":300,"enrichment":1200,"output":45}}`)
	}))
	defer server.Close()

	tm := new(token.MockTokenManager)
	tm.On("GetAccessToken", mock.Anything).Return("fake-token", nil)
	c := NewStackSpotClient(tm, "agent-id", zap.NewNop(), 1, 0)
	c.baseURL = server.URL + "/v1"

	if _, err := c.SendPrompt(context.Background(), "Hi",
		[]models.Message{{Role: "user", Content: "Hi"}}, 0); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}

	u := c.LastUsage()
	if u == nil || u.InputTokensTotal != 1500 || u.TotalTokens != 1545 {
		t.Fatalf("usage not normalized: %+v", u)
	}
}
