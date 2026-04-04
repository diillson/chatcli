/*
 * ChatCLI - GitHub Models marketplace provider
 * Uses OpenAI-compatible API at models.inference.ai.azure.com
 * Auth: GitHub Personal Access Token (PAT)
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package github_models

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
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// GitHubModelsClient implements an LLM client for the GitHub Models marketplace.
// It uses the OpenAI-compatible API at models.inference.ai.azure.com.
type GitHubModelsClient struct {
	apiKey      string
	model       string
	logger      *zap.Logger
	client      *http.Client
	maxAttempts int
	backoff     time.Duration
}

// NewGitHubModelsClient creates a new GitHub Models client.
func NewGitHubModelsClient(apiKey, model string, logger *zap.Logger, maxAttempts int, backoff time.Duration) *GitHubModelsClient {
	httpClient := utils.NewHTTPClient(logger, 900*time.Second)

	return &GitHubModelsClient{
		apiKey:      apiKey,
		model:       model,
		logger:      logger,
		client:      httpClient,
		maxAttempts: maxAttempts,
		backoff:     backoff,
	}
}

func (c *GitHubModelsClient) GetModelName() string {
	return catalog.GetDisplayName(catalog.ProviderGitHubModels, c.model)
}

func (c *GitHubModelsClient) getMaxTokens() int {
	if tokenStr := os.Getenv("GITHUB_MODELS_MAX_TOKENS"); tokenStr != "" {
		if parsed, err := strconv.Atoi(tokenStr); err == nil && parsed > 0 {
			return parsed
		}
	}
	return catalog.GetMaxTokens(catalog.ProviderGitHubModels, c.model, 4096)
}

func (c *GitHubModelsClient) getAPIURL() string {
	return utils.GetEnvOrDefault("GITHUB_MODELS_API_URL", config.GitHubModelsAPIURL)
}

func (c *GitHubModelsClient) getModelsURL() string {
	apiURL := c.getAPIURL()
	return strings.TrimSuffix(apiURL, "/chat/completions") + "/models"
}

// SendPrompt sends a prompt to the GitHub Models API.
func (c *GitHubModelsClient) SendPrompt(ctx context.Context, prompt string, history []models.Message, maxTokens int) (string, error) {
	effectiveMaxTokens := maxTokens
	if effectiveMaxTokens <= 0 {
		effectiveMaxTokens = c.getMaxTokens()
	}

	messages := []map[string]string{}
	for _, msg := range history {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		switch role {
		case "system", "user", "assistant":
		default:
			role = "user"
		}
		messages = append(messages, map[string]string{
			"role":    role,
			"content": msg.Content,
		})
	}

	if len(history) == 0 || history[len(history)-1].Role != "user" || history[len(history)-1].Content != prompt {
		if strings.TrimSpace(prompt) != "" {
			messages = append(messages, map[string]string{
				"role":    "user",
				"content": prompt,
			})
		}
	}

	payload := map[string]interface{}{
		"model":      c.model,
		"messages":   messages,
		"max_tokens": effectiveMaxTokens,
	}

	jsonValue, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("%s: %w", i18n.T("llm.error.marshal_payload_for", "GitHub Models"), err)
	}

	response, err := utils.Retry(ctx, c.logger, c.maxAttempts, c.backoff, func(ctx context.Context) (string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.getAPIURL(), utils.NewJSONReader(jsonValue))
		if err != nil {
			return "", fmt.Errorf("%s: %w", i18n.T("llm.error.create_request_for", "GitHub Models"), err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+auth.StripAuthPrefix(c.apiKey))

		resp, err := c.client.Do(req)
		if err != nil {
			return "", err
		}
		defer func() { _ = resp.Body.Close() }()

		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", fmt.Errorf("%s: %w", i18n.T("llm.error.read_response_for", "GitHub Models"), err)
		}

		if resp.StatusCode != http.StatusOK {
			return "", &utils.APIError{StatusCode: resp.StatusCode, Message: utils.SanitizeSensitiveText(string(bodyBytes))}
		}

		var result struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(bodyBytes, &result); err != nil {
			return "", fmt.Errorf("%s: %w", i18n.T("llm.error.decode_response_for", "GitHub Models"), err)
		}
		if len(result.Choices) == 0 {
			return "", fmt.Errorf("%s", i18n.T("llm.error.no_choices", "GitHub Models"))
		}
		return result.Choices[0].Message.Content, nil
	})

	return response, err
}

// ListModels fetches available models from the GitHub Models API.
// The API returns a plain JSON array (not OpenAI format) with fields:
//
//	name, friendly_name, task, publisher, model_family, etc.
//
// We filter to chat-completion models only.
func (c *GitHubModelsClient) ListModels(ctx context.Context) ([]client.ModelInfo, error) {
	modelsURL := c.getModelsURL()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.error.create_request_for", "GitHub Models"), err)
	}
	req.Header.Set("Authorization", "Bearer "+auth.StripAuthPrefix(c.apiKey))

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.error.request_failed", "GitHub Models"), err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.error.read_response_for", "GitHub Models"), err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", i18n.T("llm.error.api_error_code", "GitHub Models", resp.StatusCode, utils.SanitizeSensitiveText(string(bodyBytes))))
	}

	// GitHub Models returns a plain JSON array (NOT OpenAI {"data":[...]} format)
	type ghModel struct {
		ID           string `json:"id"`   // Azure registry path (long)
		Name         string `json:"name"` // Model name used for inference
		FriendlyName string `json:"friendly_name"`
		Publisher    string `json:"publisher"`
		ModelFamily  string `json:"model_family"`
		Task         string `json:"task"` // "chat-completion", "embeddings", etc.
	}

	// Try plain array first (actual GitHub Models format)
	var models []ghModel
	if err := json.Unmarshal(bodyBytes, &models); err != nil {
		// Fallback: try OpenAI-compatible {"data": [...]} format
		var wrapped struct {
			Data []ghModel `json:"data"`
		}
		if err2 := json.Unmarshal(bodyBytes, &wrapped); err2 != nil {
			return nil, fmt.Errorf("%s: %w", i18n.T("llm.error.decode_response_for", "GitHub Models"), err)
		}
		models = wrapped.Data
	}

	var modelList []client.ModelInfo
	for _, m := range models {
		// Filter to chat-capable models only
		if m.Task != "" && m.Task != "chat-completion" {
			continue
		}

		// Use Name for inference (not the long Azure registry ID)
		modelID := m.Name
		if modelID == "" {
			modelID = m.ID
		}

		displayName := m.FriendlyName
		if displayName == "" {
			displayName = modelID
		}
		if m.Publisher != "" {
			displayName = fmt.Sprintf("%s (%s)", displayName, m.Publisher)
		}

		modelList = append(modelList, client.ModelInfo{
			ID:          modelID,
			DisplayName: displayName,
			Source:      client.ModelSourceAPI,
		})

		// Register in catalog for downstream use
		if _, ok := catalog.Resolve(catalog.ProviderGitHubModels, modelID); !ok {
			catalog.Register(catalog.ModelMeta{
				ID:           modelID,
				Aliases:      []string{modelID},
				DisplayName:  displayName,
				Provider:     catalog.ProviderGitHubModels,
				PreferredAPI: catalog.APIChatCompletions,
			})
		}
	}

	c.logger.Info(i18n.T("llm.info.fetched_models", "GitHub Models"), zap.Int("total", len(models)), zap.Int("chat", len(modelList)))
	return modelList, nil
}
