/*
 * ChatCLI - Live image-model discovery.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The static catalog goes stale: providers ship image models between
 * releases (OpenAI's gpt-image-2.5 family, xAI's quality tier, Google's
 * previews) and retire others. Every provider that exposes a listing is asked
 * directly, so @image models and /config image models show what the account
 * can actually call right now, and the catalog only adds what a listing
 * cannot say (edit support, lifecycle, notes).
 */
package imagegen

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"

	bedrocksvc "github.com/aws/aws-sdk-go-v2/service/bedrock"
	bedrocktypes "github.com/aws/aws-sdk-go-v2/service/bedrock/types"
	"github.com/diillson/chatcli/llm/bedrock"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// LiveModel is an image model a provider's own API reports.
type LiveModel struct {
	ID string
	// Regions lists where a Bedrock model is offered (empty elsewhere).
	Regions []string
	// ProfileOnly marks a Bedrock model that is invoked only through a
	// cross-region inference profile ("us.<id>").
	ProfileOnly bool
}

// Endpoints for the listings. Variables so tests can point them at a local
// server.
var (
	xaiImageModelsURL = "https://api.x.ai/v1/image-generation-models"
	googleModelsURL   = googleImageBase + "/v1beta/models?pageSize=1000"
)

// LiveListable reports whether provider has a model listing API.
func LiveListable(provider string) bool {
	switch provider {
	case "openai", "xai", "google", "bedrock":
		return true
	}
	return false
}

// ListLiveModels asks provider for the image models the configured account
// can use. It returns (nil, nil) when the provider has no listing or no
// credential is configured, so callers fall back to the catalog.
func ListLiveModels(ctx context.Context, provider string, logger *zap.Logger) ([]LiveModel, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	switch provider {
	case "openai":
		key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
		if key == "" {
			return nil, nil
		}
		ids, err := FetchOpenAIModels(ctx, openAIBaseURL, key, logger)
		if err != nil {
			return nil, err
		}
		var out []LiveModel
		for _, id := range ids {
			// The Images API models; chat models usable through the
			// Responses API are a different backend.
			if strings.Contains(strings.ToLower(id), "image") {
				out = append(out, LiveModel{ID: id})
			}
		}
		return out, nil
	case "xai":
		key := strings.TrimSpace(os.Getenv("XAI_API_KEY"))
		if key == "" {
			return nil, nil
		}
		return listXAIImageModels(ctx, key, logger)
	case "google":
		key := googleImageKey()
		if key == "" {
			return nil, nil
		}
		return listGoogleImageModels(ctx, key, logger)
	case "bedrock":
		return listBedrockImageModels(ctx, logger)
	}
	return nil, nil
}

func getJSON(ctx context.Context, logger *zap.Logger, url string, headers map[string]string, v interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	for k, val := range headers {
		req.Header.Set(k, val)
	}
	resp, err := utils.NewHTTPClientH1(logger, imageGenTimeout).Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
		return fmt.Errorf("model listing returned %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// listXAIImageModels reads GET /v1/image-generation-models.
func listXAIImageModels(ctx context.Context, key string, logger *zap.Logger) ([]LiveModel, error) {
	var out struct {
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	if err := getJSON(ctx, logger, xaiImageModelsURL, map[string]string{"Authorization": "Bearer " + key}, &out); err != nil {
		return nil, err
	}
	models := make([]LiveModel, 0, len(out.Models))
	for _, m := range out.Models {
		if m.ID != "" {
			models = append(models, LiveModel{ID: m.ID})
		}
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

// listGoogleImageModels reads the Gemini API model list and keeps the image
// models (their ids carry "image" or "banana", as in gemini-nano-banana-2.1).
func listGoogleImageModels(ctx context.Context, key string, logger *zap.Logger) ([]LiveModel, error) {
	var out struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := getJSON(ctx, logger, googleModelsURL, map[string]string{"x-goog-api-key": key}, &out); err != nil {
		return nil, err
	}
	var models []LiveModel
	for _, m := range out.Models {
		id := strings.TrimPrefix(m.Name, "models/")
		if strings.Contains(id, "image") || strings.Contains(id, "banana") {
			models = append(models, LiveModel{ID: id})
		}
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

// bedrockListRegions are where Bedrock image models are offered today; the
// configured region is asked too, so a new region shows up on its own.
var bedrockListRegions = []string{"us-west-2", "us-east-1"}

// listBedrockImageModels lists the IMAGE-output foundation models in the
// configured region and in the regions that host Bedrock's image models,
// merging them by id with the regions each one is offered in.
func listBedrockImageModels(ctx context.Context, logger *zap.Logger) ([]LiveModel, error) {
	regions := append([]string(nil), bedrockListRegions...)
	if r := bedrockImageRegion(); r != "" && !containsString(regions, r) {
		regions = append([]string{r}, regions...)
	}
	byID := map[string]*LiveModel{}
	var firstErr error
	for _, region := range regions {
		client, _, err := bedrock.LoadBedrockControl(ctx, region, bedrockImageProfile(), logger)
		if err != nil {
			return nil, nil // no AWS configuration: not an error for a listing
		}
		out, err := client.ListFoundationModels(ctx, &bedrocksvc.ListFoundationModelsInput{
			ByOutputModality: bedrocktypes.ModelModalityImage,
		})
		if err != nil {
			if firstErr == nil {
				firstErr = bedrock.ExplainCredentialError(err)
			}
			continue
		}
		for _, s := range out.ModelSummaries {
			if s.ModelId == nil {
				continue
			}
			if s.ModelLifecycle != nil && s.ModelLifecycle.Status != bedrocktypes.FoundationModelLifecycleStatusActive {
				continue
			}
			m, ok := byID[*s.ModelId]
			if !ok {
				m = &LiveModel{ID: *s.ModelId, ProfileOnly: profileOnly(s.InferenceTypesSupported)}
				byID[*s.ModelId] = m
			}
			m.Regions = append(m.Regions, region)
		}
	}
	if len(byID) == 0 && firstErr != nil {
		return nil, firstErr
	}
	models := make([]LiveModel, 0, len(byID))
	for _, m := range byID {
		models = append(models, *m)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

func profileOnly(types []bedrocktypes.InferenceType) bool {
	for _, t := range types {
		if t == bedrocktypes.InferenceTypeOnDemand {
			return false
		}
	}
	return len(types) > 0
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
