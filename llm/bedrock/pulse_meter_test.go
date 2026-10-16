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
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	bedrocksvc "github.com/aws/aws-sdk-go-v2/service/bedrock"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/pkg/pulse/pulsetest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// connTo matches the end of a connection node to host.
func connTo(host string) func(pulse.Event) bool {
	return pulsetest.Is(pulse.KindConn, host, pulse.PhaseEnd)
}

func TestBedrockRuntimeRequestsShowAsConnections(t *testing.T) {
	clearEndpointEnv(t)
	hermeticAWSEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	t.Setenv("BEDROCK_BASE_URL", srv.URL+"/secret-path")
	rec := pulsetest.Record(t)

	rt, _, err := LoadBedrockRuntime(context.Background(), "", "", zap.NewNop())
	require.NoError(t, err)
	metered, ok := rt.Options().HTTPClient.(*meteredAWSClient)
	require.True(t, ok, "runtime client must be metered, got %T", rt.Options().HTTPClient)
	_, ok = metered.unwrap().(*awshttp.BuildableClient)
	assert.True(t, ok, "the SDK's own resolved client must still do the work")

	_, err = rt.InvokeModel(context.Background(), &bedrockruntime.InvokeModelInput{
		ModelId:     aws.String("anthropic.claude-test"),
		Body:        []byte(`{"prompt":"SECRET-PROMPT"}`),
		ContentType: aws.String("application/json"),
	})
	require.NoError(t, err)

	ev := rec.WaitFor(t, "runtime conn", connTo("127.0.0.1"))
	assert.Equal(t, "POST", ev.Attrs["method"])
	assert.Equal(t, "200", ev.Attrs["status"])
	assert.Equal(t, pulse.StatusOK, ev.Status)
	for _, e := range rec.Events() {
		for k, v := range e.Attrs {
			assert.NotContains(t, v, "secret-path", "attr %s leaked the path", k)
			assert.NotContains(t, v, "SECRET-PROMPT", "attr %s leaked the body", k)
		}
		assert.False(t, strings.Contains(e.Name, "secret-path"))
	}
}

func TestBedrockControlRequestsShowAsConnections(t *testing.T) {
	clearEndpointEnv(t)
	hermeticAWSEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"modelSummaries":[]}`))
	}))
	defer srv.Close()
	t.Setenv("BEDROCK_CONTROL_BASE_URL", srv.URL)
	rec := pulsetest.Record(t)

	ctl, _, err := LoadBedrockControl(context.Background(), "", "", zap.NewNop())
	require.NoError(t, err)
	_, err = ctl.ListFoundationModels(context.Background(), &bedrocksvc.ListFoundationModelsInput{})
	require.NoError(t, err)
	ev := rec.WaitFor(t, "control conn", connTo("127.0.0.1"))
	assert.Equal(t, "GET", ev.Attrs["method"])

	// The chat client's own control plane is metered too.
	c := NewBedrockClient("anthropic.claude-sonnet-5", "", "", zap.NewNop(), 1, 0)
	require.NoError(t, c.ensureRuntime(context.Background()))
	_, ok := c.control.Options().HTTPClient.(*meteredAWSClient)
	assert.True(t, ok, "chat control plane must be metered, got %T", c.control.Options().HTTPClient)
	_, ok = c.runtime.Options().HTTPClient.(*meteredAWSClient)
	assert.True(t, ok, "chat runtime must be metered, got %T", c.runtime.Options().HTTPClient)
}

func TestMeterAWSHTTPClientIsIdempotent(t *testing.T) {
	assert.Nil(t, meterAWSHTTPClient(nil))
	once := meterAWSHTTPClient(awshttp.NewBuildableClient())
	assert.Same(t, once, meterAWSHTTPClient(once))
}
