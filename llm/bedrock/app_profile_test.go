/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package bedrock

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	bedrocksvc "github.com/aws/aws-sdk-go-v2/service/bedrock"
	bedrocktypes "github.com/aws/aws-sdk-go-v2/service/bedrock/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/diillson/chatcli/llm/catalog"
)

// newFakeEndpointControl builds a bedrock control-plane client aimed at a
// local httptest server, mirroring newFakeEndpointRuntime.
func newFakeEndpointControl(baseURL string) *bedrocksvc.Client {
	cfg := aws.Config{
		Region: "us-east-1",
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "test-key", SecretAccessKey: "test-secret"}, nil
		}),
	}
	return bedrocksvc.NewFromConfig(cfg, func(o *bedrocksvc.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

func TestFirstProfileModelID(t *testing.T) {
	arn := "arn:aws:bedrock:us-east-1::foundation-model/global.anthropic.claude-opus-4-8"
	models := []bedrocktypes.InferenceProfileModel{{ModelArn: &arn}}
	assert.Equal(t, "global.anthropic.claude-opus-4-8", firstProfileModelID(models))

	assert.Empty(t, firstProfileModelID(nil))
	assert.Empty(t, firstProfileModelID([]bedrocktypes.InferenceProfileModel{{ModelArn: nil}}))

	// The first non-nil ARN wins; malformed ARNs without a slash are skipped
	// by the extraction (empty segment after the last slash).
	bad := "no-slash-at-all"
	good := "arn:aws:bedrock:eu-west-1::foundation-model/amazon.nova-pro-v1:0"
	assert.Equal(t, "amazon.nova-pro-v1:0",
		firstProfileModelID([]bedrocktypes.InferenceProfileModel{{ModelArn: &bad}, {ModelArn: &good}}))
}

// An application inference profile ARN carries zero provider information in
// the string. Registration with the underlying model id must (a) inherit
// window/output specs, (b) route family dispatch onto the Anthropic path so
// cache_control survives, and (c) drop bedrock_mantle_only — the Mantle
// endpoint would 404 on an ARN.
func TestRegisterBedrockModelWithUnderlyingProfile(t *testing.T) {
	arn := "arn:aws:bedrock:us-east-1:111122223333:application-inference-profile/reg-test-opus"
	registerBedrockModel(arn, "cost tracking opus", "global.anthropic.claude-opus-4-8")

	meta, ok := catalog.Resolve(catalog.ProviderBedrock, arn)
	require.True(t, ok)
	assert.Equal(t, 1000000, meta.ContextWindow, "profile must inherit the underlying model's context window")
	assert.Equal(t, 128000, meta.MaxOutputTokens, "profile must inherit the underlying model's output ceiling")
	assert.Equal(t, catalog.APIAnthropicMessages, meta.PreferredAPI)

	assert.Equal(t, familyAnthropic, resolveFamily(arn),
		"registered Claude profile ARNs must dispatch through the Anthropic path")

	assert.Equal(t, 128000, catalog.GetMaxTokens(catalog.ProviderBedrock, arn, 0))
	assert.Equal(t, 1000000, catalog.GetContextWindow(catalog.ProviderBedrock, arn))
}

func TestRegisterBedrockModelStripsMantleOnlyForProfiles(t *testing.T) {
	arn := "arn:aws:bedrock:us-east-1:111122223333:application-inference-profile/reg-test-sonnet5"
	registerBedrockModel(arn, "cost tracking sonnet", "anthropic.claude-sonnet-5")

	meta, ok := catalog.Resolve(catalog.ProviderBedrock, arn)
	require.True(t, ok)
	assert.Equal(t, 128000, meta.MaxOutputTokens)
	assert.NotContains(t, meta.Capabilities, "bedrock_mantle_only",
		"Mantle serves canonical ids only; a profile ARN must stay on InvokeModel")
	assert.False(t, usesMantleEndpoint(arn))
}

// An unregistered profile ARN must stay on the conservative fallbacks —
// that is exactly the degradation the lazy lookup exists to remove.
func TestUnresolvedProfileARNFallsBack(t *testing.T) {
	arn := "arn:aws:bedrock:us-east-1:111122223333:application-inference-profile/never-registered"
	assert.Equal(t, familyConverse, resolveFamily(arn))
	assert.Equal(t, 8192, catalog.GetMaxTokens(catalog.ProviderBedrock, arn, 0))
}

func TestMaybeResolveProfileModel(t *testing.T) {
	arn := "arn:aws:bedrock:us-east-1:111122223333:application-inference-profile/lazy-test"
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"inferenceProfileArn": "` + arn + `",
			"inferenceProfileId": "lazy-test",
			"inferenceProfileName": "team cost profile",
			"models": [{"modelArn": "arn:aws:bedrock:us-east-1::foundation-model/global.anthropic.claude-opus-4-8"}],
			"status": "ACTIVE",
			"type": "APPLICATION"
		}`))
	}))
	defer srv.Close()

	c := &BedrockClient{
		model:   arn,
		logger:  zap.NewNop(),
		control: newFakeEndpointControl(srv.URL),
	}
	c.maybeResolveProfileModel(context.Background())

	meta, ok := catalog.Resolve(catalog.ProviderBedrock, arn)
	require.True(t, ok, "lazy lookup must register the profile in the catalog")
	assert.Equal(t, 128000, meta.MaxOutputTokens)
	assert.Equal(t, 1000000, meta.ContextWindow)
	assert.Equal(t, familyAnthropic, resolveFamily(arn))
	assert.Equal(t, 1, calls)

	// Second call short-circuits on the catalog hit — no extra API call.
	c.maybeResolveProfileModel(context.Background())
	assert.Equal(t, 1, calls)
}

func TestMaybeResolveProfileModelSkipsNonProfileAndFailure(t *testing.T) {
	// Non-profile ids never trigger a control-plane call (control is nil —
	// a call would panic).
	c := &BedrockClient{model: "anthropic.claude-sonnet-5", logger: zap.NewNop()}
	c.maybeResolveProfileModel(context.Background())

	// A failing lookup is attempted once, logged, and never retried.
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	failing := &BedrockClient{
		model:   "arn:aws:bedrock:us-east-1:111122223333:application-inference-profile/denied",
		logger:  zap.NewNop(),
		control: newFakeEndpointControl(srv.URL),
	}
	failing.maybeResolveProfileModel(context.Background())
	failing.maybeResolveProfileModel(context.Background())
	assert.Equal(t, 1, calls, "failed lookups must not retry on every prompt")
}

func TestAppendInferenceProfilesApplication(t *testing.T) {
	arn := "arn:aws:bedrock:us-east-1:111122223333:application-inference-profile/list-test"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "APPLICATION", r.URL.Query().Get("type"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"inferenceProfileSummaries": [{
				"inferenceProfileArn": "` + arn + `",
				"inferenceProfileId": "list-test",
				"inferenceProfileName": "list profile",
				"models": [{"modelArn": "arn:aws:bedrock:us-east-1::foundation-model/anthropic.claude-fable-5"}],
				"status": "ACTIVE",
				"type": "APPLICATION"
			}]
		}`))
	}))
	defer srv.Close()

	c := &BedrockClient{logger: zap.NewNop(), control: newFakeEndpointControl(srv.URL)}
	seen := map[string]bool{}
	result := c.appendInferenceProfiles(context.Background(), bedrocktypes.InferenceProfileTypeApplication, seen, nil)

	require.Len(t, result, 1)
	assert.Equal(t, arn, result[0].ID, "application profiles must be selected by ARN — that is the invokable id")

	meta, ok := catalog.Resolve(catalog.ProviderBedrock, arn)
	require.True(t, ok)
	assert.Equal(t, 128000, meta.MaxOutputTokens, "listing must inherit specs from the profile's underlying model")

	// A second sweep with the same seen map adds nothing.
	again := c.appendInferenceProfiles(context.Background(), bedrocktypes.InferenceProfileTypeApplication, seen, result)
	assert.Len(t, again, 1)
}

func TestProfileEntrySystemDefinedKeepsID(t *testing.T) {
	id := "us.anthropic.claude-opus-4-8"
	arn := "arn:aws:bedrock:us-east-1:111122223333:inference-profile/us.anthropic.claude-opus-4-8"
	name := "US Anthropic Claude Opus 4.8"
	p := bedrocktypes.InferenceProfileSummary{
		InferenceProfileArn:  &arn,
		InferenceProfileId:   &id,
		InferenceProfileName: &name,
	}
	gotID, _, underlying := profileEntry(p, bedrocktypes.InferenceProfileTypeSystemDefined)
	assert.Equal(t, id, gotID, "system-defined profiles keep their id spelling")
	assert.Empty(t, underlying)
}

// The Converse and OpenAI dispatch paths used a flat 4096 whenever the
// caller passed maxTokens=0 (agent/coder always do) — ignoring both the
// catalog entry of the model and BEDROCK_MAX_TOKENS precedence.
func TestGetMaxTokensConverseUsesCatalog(t *testing.T) {
	c := &BedrockClient{model: "us.amazon.nova-premier-v1:0", logger: zap.NewNop()}
	assert.Equal(t, 5120, c.getMaxTokensConverse(), "cataloged Converse models must use their real ceiling")

	unknown := &BedrockClient{model: "meta.llama9-hypothetical-v1:0", logger: zap.NewNop()}
	assert.Equal(t, 8192, unknown.getMaxTokensConverse(), "unknown Converse models take the family fallback")

	t.Setenv("BEDROCK_MAX_TOKENS", "2048")
	assert.Equal(t, 2048, c.getMaxTokensConverse(), "operator env override keeps top precedence")
}

func TestGetMaxTokensOpenAIUsesCatalog(t *testing.T) {
	c := &BedrockClient{model: "openai.gpt-oss-120b-1:0", logger: zap.NewNop()}
	assert.Equal(t, 16384, c.getMaxTokensOpenAI(), "cataloged gpt-oss must use its real ceiling")

	t.Setenv("BEDROCK_MAX_TOKENS", "1234")
	assert.Equal(t, 1234, c.getMaxTokensOpenAI())
}
