/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"github.com/diillson/chatcli/version"
	"go.uber.org/zap"
)

const (
	// CopilotAPIBaseURL is the base URL for the GitHub Copilot Chat API.
	CopilotAPIBaseURL = "https://api.githubcopilot.com"

	// CopilotChatCompletionsPath is the chat completions endpoint.
	CopilotChatCompletionsPath = "/chat/completions"
)

// Client implements the LLMClient interface for GitHub Copilot.
type Client struct {
	token       string
	model       string
	logger      *zap.Logger
	client      *http.Client
	maxAttempts int
	backoff     time.Duration
	baseURL     string
}

// NewClient creates a new GitHub Copilot client.
func NewClient(token, model string, logger *zap.Logger, maxAttempts int, backoff time.Duration) *Client {
	httpClient := utils.NewHTTPClient(logger, 900*time.Second)

	baseURL := CopilotAPIBaseURL
	if env := os.Getenv("COPILOT_API_BASE_URL"); env != "" {
		baseURL = strings.TrimRight(env, "/")
	}

	return &Client{
		token:       token,
		model:       model,
		logger:      logger,
		client:      httpClient,
		maxAttempts: maxAttempts,
		backoff:     backoff,
		baseURL:     baseURL,
	}
}

// GetModelName returns the friendly display name for the model via catalog.
func (c *Client) GetModelName() string {
	return catalog.GetDisplayName(catalog.ProviderCopilot, c.model)
}

func (c *Client) getMaxTokens() int {
	if tokenStr := os.Getenv("COPILOT_MAX_TOKENS"); tokenStr != "" {
		if parsedTokens, err := strconv.Atoi(tokenStr); err == nil && parsedTokens > 0 {
			return parsedTokens
		}
	}
	return catalog.GetMaxTokens(catalog.ProviderCopilot, c.model, 0)
}

// SendPrompt sends a prompt to the GitHub Copilot API and returns the response.
func (c *Client) SendPrompt(ctx context.Context, prompt string, history []models.Message, maxTokens int) (string, error) {
	effectiveMaxTokens := maxTokens
	if effectiveMaxTokens <= 0 {
		effectiveMaxTokens = c.getMaxTokens()
	}

	messages := make([]map[string]string, 0, len(history)+1)
	for _, msg := range history {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		switch role {
		case "system", "user", "assistant":
			// valid
		default:
			role = "user"
		}
		messages = append(messages, map[string]string{
			"role":    role,
			"content": msg.Content,
		})
	}

	// Append the current prompt if not already the last message
	if len(history) == 0 || history[len(history)-1].Role != "user" || history[len(history)-1].Content != prompt {
		if strings.TrimSpace(prompt) != "" {
			messages = append(messages, map[string]string{
				"role":    "user",
				"content": prompt,
			})
		}
	}

	payload := map[string]interface{}{
		"model":    c.model,
		"messages": messages,
		"stream":   false,
		"store":    false, // Copilot API: don't store conversations
	}

	// Copilot API: do NOT send max_tokens / maxOutputTokens
	// as per the API contract. The API manages token limits internally.
	// Only set if user explicitly configured it.
	if os.Getenv("COPILOT_MAX_TOKENS") != "" {
		payload["max_tokens"] = effectiveMaxTokens
	}

	jsonValue, err := json.Marshal(payload)
	if err != nil {
		c.logger.Error("Failed to marshal payload", zap.Error(err))
		return "", fmt.Errorf("failed to prepare request: %w", err)
	}

	response, err := utils.Retry(ctx, c.logger, c.maxAttempts, c.backoff, func(ctx context.Context) (string, error) {
		resp, err := c.sendRequest(ctx, jsonValue)
		if err != nil {
			return "", err
		}
		return c.processResponse(resp)
	})

	return response, err
}

// sendRequest sends the HTTP request to the Copilot API with required headers.
func (c *Client) sendRequest(ctx context.Context, jsonValue []byte) (*http.Response, error) {
	apiURL := c.baseURL + CopilotChatCompletionsPath

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, utils.NewJSONReader(jsonValue))
	if err != nil {
		c.logger.Error("Failed to create request", zap.Error(err))
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Required headers for Copilot API
	token := auth.StripAuthPrefix(c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Openai-Intent", "conversation-edits")
	req.Header.Set("X-Initiator", "user")

	// User-Agent with version
	ver, _, _ := version.GetBuildInfo()
	req.Header.Set("User-Agent", "chatcli/"+ver)

	// Editor identification (required by some Copilot API versions)
	req.Header.Set("Editor-Version", "chatcli/"+ver)
	req.Header.Set("Editor-Plugin-Version", "chatcli/"+ver)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}

	return resp, nil
}

// processResponse processes the Copilot API response (OpenAI-compatible format).
func (c *Client) processResponse(resp *http.Response) (string, error) {
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		c.logger.Error("Failed to read Copilot response", zap.Error(err))
		return "", fmt.Errorf("failed to read Copilot response: %w", err)
	}

	if resp.StatusCode == http.StatusForbidden {
		return "", &utils.APIError{
			StatusCode: resp.StatusCode,
			Message:    "GitHub Copilot: access denied (403). Your Copilot subscription may have expired or the token is invalid. Run '/auth login github-copilot' to re-authenticate.",
		}
	}

	if resp.StatusCode != http.StatusOK {
		return "", &utils.APIError{
			StatusCode: resp.StatusCode,
			Message:    utils.SanitizeSensitiveText(string(bodyBytes)),
		}
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		c.logger.Error("Failed to decode Copilot response", zap.Error(err))
		return "", fmt.Errorf("failed to decode Copilot response: %w", err)
	}

	if len(result.Choices) == 0 {
		c.logger.Error("No response from Copilot", zap.String("body", string(bodyBytes)))
		return "", fmt.Errorf("no response received from GitHub Copilot")
	}

	return result.Choices[0].Message.Content, nil
}
