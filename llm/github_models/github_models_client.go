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
		return "", fmt.Errorf("marshaling payload: %w", err)
	}

	response, err := utils.Retry(ctx, c.logger, c.maxAttempts, c.backoff, func(ctx context.Context) (string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.getAPIURL(), utils.NewJSONReader(jsonValue))
		if err != nil {
			return "", fmt.Errorf("creating request: %w", err)
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
			return "", fmt.Errorf("reading response: %w", err)
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
			return "", fmt.Errorf("decoding response: %w", err)
		}
		if len(result.Choices) == 0 {
			return "", fmt.Errorf("no choices in response")
		}
		return result.Choices[0].Message.Content, nil
	})

	return response, err
}

// ListModels fetches available models from the GitHub Models API.
func (c *GitHubModelsClient) ListModels(ctx context.Context) ([]client.ModelInfo, error) {
	modelsURL := c.getModelsURL()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+auth.StripAuthPrefix(c.apiKey))

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching models: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub Models /models returned %d: %s", resp.StatusCode, utils.SanitizeSensitiveText(string(bodyBytes)))
	}

	// GitHub Models returns OpenAI-compatible format: {"data": [{"id": "..."}]}
	var result struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return nil, fmt.Errorf("decoding models: %w", err)
	}

	var modelList []client.ModelInfo
	for _, m := range result.Data {
		modelList = append(modelList, client.ModelInfo{
			ID:          m.ID,
			DisplayName: m.ID,
			Source:      client.ModelSourceAPI,
		})

		// Register in catalog for downstream use
		if _, ok := catalog.Resolve(catalog.ProviderGitHubModels, m.ID); !ok {
			catalog.Register(catalog.ModelMeta{
				ID:           m.ID,
				Aliases:      []string{m.ID},
				DisplayName:  m.ID,
				Provider:     catalog.ProviderGitHubModels,
				PreferredAPI: catalog.APIChatCompletions,
			})
		}
	}

	c.logger.Info("Fetched GitHub Models", zap.Int("count", len(modelList)))
	return modelList, nil
}
