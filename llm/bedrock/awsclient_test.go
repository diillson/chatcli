/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package bedrock

import (
	"context"
	"path/filepath"
	"testing"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// clearEndpointEnv guarantees each test starts without endpoint overrides
// leaking in from the developer's shell.
func clearEndpointEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"BEDROCK_BASE_URL",
		"BEDROCK_CONTROL_BASE_URL",
		"AWS_ENDPOINT_URL_BEDROCK_RUNTIME",
		"AWS_ENDPOINT_URL_BEDROCK",
		"AWS_ENDPOINT_URL",
		"AWS_IGNORE_CONFIGURED_ENDPOINT_URLS",
	} {
		t.Setenv(key, "")
	}
}

// hermeticAWSEnv pins region + static credentials and detaches the SDK from
// the developer's real ~/.aws files and from IMDS, so LoadDefaultConfig
// resolves deterministically offline.
func hermeticAWSEnv(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAIOSFODNN7EXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
}

func TestRuntimeEndpointURLDefault(t *testing.T) {
	clearEndpointEnv(t)
	assert.Equal(t, "https://bedrock-runtime.us-east-1.amazonaws.com", RuntimeEndpointURL("us-east-1"))
	assert.Empty(t, RuntimeEndpointURL(""))
}

func TestRuntimeEndpointURLBedrockBaseURLWins(t *testing.T) {
	clearEndpointEnv(t)
	t.Setenv("BEDROCK_BASE_URL", "https://bedrock.internal.example.com/")
	t.Setenv("AWS_ENDPOINT_URL_BEDROCK_RUNTIME", "https://sdk-native.example.com")
	assert.Equal(t, "https://bedrock.internal.example.com", RuntimeEndpointURL("us-east-1"))
	// The override also surfaces when the region is still unresolved.
	assert.Equal(t, "https://bedrock.internal.example.com", RuntimeEndpointURL(""))
}

func TestRuntimeEndpointURLReflectsAWSStandardVars(t *testing.T) {
	clearEndpointEnv(t)
	t.Setenv("AWS_ENDPOINT_URL_BEDROCK_RUNTIME", "https://vpce.example.internal/")
	assert.Equal(t, "https://vpce.example.internal", RuntimeEndpointURL("us-east-1"))

	t.Setenv("AWS_ENDPOINT_URL_BEDROCK_RUNTIME", "")
	t.Setenv("AWS_ENDPOINT_URL", "https://all-services.example.internal")
	assert.Equal(t, "https://all-services.example.internal", RuntimeEndpointURL("us-east-1"))

	// The AWS-standard ignore switch restores the regional default…
	t.Setenv("AWS_IGNORE_CONFIGURED_ENDPOINT_URLS", "true")
	assert.Equal(t, "https://bedrock-runtime.us-east-1.amazonaws.com", RuntimeEndpointURL("us-east-1"))

	// …but never silences the chatcli-native variable, which is applied as
	// BaseEndpoint in code, outside the SDK's env-endpoint resolution.
	t.Setenv("BEDROCK_BASE_URL", "https://bedrock.internal.example.com")
	assert.Equal(t, "https://bedrock.internal.example.com", RuntimeEndpointURL("us-east-1"))
}

func TestNormalizeBaseURL(t *testing.T) {
	got, err := normalizeBaseURL("  https://bedrock.internal.example.com/  ")
	require.NoError(t, err)
	assert.Equal(t, "https://bedrock.internal.example.com", got)

	got, err = normalizeBaseURL("http://localhost:8443/bedrock")
	require.NoError(t, err)
	assert.Equal(t, "http://localhost:8443/bedrock", got)

	for _, bad := range []string{"bedrock.internal.example.com", "ftp://x.example.com", "https://", "://nope"} {
		_, err := normalizeBaseURL(bad)
		assert.Error(t, err, "expected %q to be rejected", bad)
	}
}

func TestLoadBedrockRuntimeAppliesBedrockBaseURL(t *testing.T) {
	clearEndpointEnv(t)
	hermeticAWSEnv(t)
	t.Setenv("BEDROCK_BASE_URL", "https://bedrock.internal.example.com/")

	client, region, err := LoadBedrockRuntime(context.Background(), "", "", zap.NewNop())
	require.NoError(t, err)
	assert.Equal(t, "us-east-1", region)
	opts := client.Options()
	require.NotNil(t, opts.BaseEndpoint, "BEDROCK_BASE_URL must land on the client as BaseEndpoint")
	assert.Equal(t, "https://bedrock.internal.example.com", *opts.BaseEndpoint)
}

func TestLoadBedrockRuntimeHonorsSDKNativeEndpointVar(t *testing.T) {
	// Proves the documented claim that the AWS-standard variable flows
	// through NewFromConfig without any chatcli-side plumbing.
	clearEndpointEnv(t)
	hermeticAWSEnv(t)
	t.Setenv("AWS_ENDPOINT_URL_BEDROCK_RUNTIME", "https://vpce.example.internal")

	client, _, err := LoadBedrockRuntime(context.Background(), "", "", zap.NewNop())
	require.NoError(t, err)
	opts := client.Options()
	require.NotNil(t, opts.BaseEndpoint)
	assert.Equal(t, "https://vpce.example.internal", *opts.BaseEndpoint)
}

func TestEnsureRuntimeControlInheritsBaseURL(t *testing.T) {
	// One variable covers the whole surface: with only BEDROCK_BASE_URL
	// set, both the runtime and the control-plane clients point at it.
	clearEndpointEnv(t)
	hermeticAWSEnv(t)
	t.Setenv("BEDROCK_BASE_URL", "https://bedrock.internal.example.com")

	c := NewBedrockClient("anthropic.claude-sonnet-5", "", "", zap.NewNop(), 1, 0)
	require.NoError(t, c.ensureRuntime(context.Background()))

	runtimeOpts := c.runtime.Options()
	require.NotNil(t, runtimeOpts.BaseEndpoint)
	assert.Equal(t, "https://bedrock.internal.example.com", *runtimeOpts.BaseEndpoint)

	controlOpts := c.control.Options()
	require.NotNil(t, controlOpts.BaseEndpoint)
	assert.Equal(t, "https://bedrock.internal.example.com", *controlOpts.BaseEndpoint)
}

func TestEnsureRuntimeControlOverrideWins(t *testing.T) {
	// Per-plane override for split hosts (AWS VPC interface endpoints are
	// created per service): BEDROCK_CONTROL_BASE_URL beats the inherited
	// BEDROCK_BASE_URL on the control plane only.
	clearEndpointEnv(t)
	hermeticAWSEnv(t)
	t.Setenv("BEDROCK_BASE_URL", "https://bedrock-runtime.internal.example.com")
	t.Setenv("BEDROCK_CONTROL_BASE_URL", "https://bedrock-control.internal.example.com/")

	c := NewBedrockClient("anthropic.claude-sonnet-5", "", "", zap.NewNop(), 1, 0)
	require.NoError(t, c.ensureRuntime(context.Background()))

	controlOpts := c.control.Options()
	require.NotNil(t, controlOpts.BaseEndpoint)
	assert.Equal(t, "https://bedrock-control.internal.example.com", *controlOpts.BaseEndpoint)

	runtimeOpts := c.runtime.Options()
	require.NotNil(t, runtimeOpts.BaseEndpoint)
	assert.Equal(t, "https://bedrock-runtime.internal.example.com", *runtimeOpts.BaseEndpoint)
}

func TestEnsureRuntimeControlPlaneHonorsSDKNativeVar(t *testing.T) {
	clearEndpointEnv(t)
	hermeticAWSEnv(t)
	t.Setenv("AWS_ENDPOINT_URL_BEDROCK", "https://bedrock-control.vpc.internal")

	c := NewBedrockClient("anthropic.claude-sonnet-5", "", "", zap.NewNop(), 1, 0)
	require.NoError(t, c.ensureRuntime(context.Background()))

	controlOpts := c.control.Options()
	require.NotNil(t, controlOpts.BaseEndpoint)
	assert.Equal(t, "https://bedrock-control.vpc.internal", *controlOpts.BaseEndpoint)
}

func TestEnsureRuntimeControlPlaneGetsCorporateHTTPClient(t *testing.T) {
	// Both planes must build from the same LoadOptions: a corporate
	// TLS override configured for Bedrock has to reach the control-plane
	// client too, or model listing breaks behind TLS-intercepting proxies
	// while chat works.
	clearEndpointEnv(t)
	hermeticAWSEnv(t)
	t.Setenv("CHATCLI_BEDROCK_INSECURE_SKIP_VERIFY", "true")

	c := NewBedrockClient("anthropic.claude-sonnet-5", "", "", zap.NewNop(), 1, 0)
	require.NoError(t, c.ensureRuntime(context.Background()))

	assertCorporateTLS := func(name string, httpClient interface{}) {
		bc, ok := httpClient.(*awshttp.BuildableClient)
		require.True(t, ok, "%s: expected the corporate BuildableClient, got %T", name, httpClient)
		tr := bc.GetTransport()
		require.NotNil(t, tr.TLSClientConfig, "%s: corporate transport must carry a TLS config", name)
		assert.True(t, tr.TLSClientConfig.InsecureSkipVerify,
			"%s: the corporate TLS override did not reach this client", name)
	}
	assertCorporateTLS("runtime", c.runtime.Options().HTTPClient)
	assertCorporateTLS("control", c.control.Options().HTTPClient)
}

func TestListModelsDegradesGracefullyWhenControlPlaneUnreachable(t *testing.T) {
	// A corporate network that blocks the control-plane host must not
	// break /switch: listing warns and returns empty so the manager falls
	// back to the catalog. Port 9 (discard) refuses immediately, keeping
	// the test fast while exercising the same failure path.
	clearEndpointEnv(t)
	hermeticAWSEnv(t)
	t.Setenv("BEDROCK_CONTROL_BASE_URL", "http://127.0.0.1:9")

	c := NewBedrockClient("anthropic.claude-sonnet-5", "", "", zap.NewNop(), 1, 0)
	models, err := c.ListModels(context.Background())
	require.NoError(t, err, "listing failures must degrade to the catalog, not error out")
	assert.Empty(t, models)
}

func TestLoadBedrockRuntimeRejectsInvalidBaseURL(t *testing.T) {
	clearEndpointEnv(t)
	hermeticAWSEnv(t)
	t.Setenv("BEDROCK_BASE_URL", "bedrock.internal.example.com")

	_, _, err := LoadBedrockRuntime(context.Background(), "", "", zap.NewNop())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "BEDROCK_BASE_URL")
}
