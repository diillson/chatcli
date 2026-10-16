/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Bedrock on the live dashboard. The AWS SDK sends through its own HTTP
 * client, which the shared metered helpers never see, so every Bedrock
 * request — chat, embeddings, images, model listing — was missing from the
 * connection view. The meter is applied as a service-client option: those
 * run after the SDK has resolved its HTTP client (defaults-mode timeouts,
 * the service read timeout, AWS_CA_BUNDLE, the corporate TLS override), so
 * the client that does the work is exactly the one it would have used, only
 * observed. Only the host, the method, the status and byte counts travel.
 */
package bedrock

import (
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
	bedrocksvc "github.com/aws/aws-sdk-go-v2/service/bedrock"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/diillson/chatcli/pkg/pulse"
)

// meteredAWSClient is an aws.HTTPClient whose requests show as connection
// nodes on the live dashboard. It delegates every request to the client it
// wraps, unchanged.
type meteredAWSClient struct {
	inner aws.HTTPClient
	rt    http.RoundTripper
}

// Do implements aws.HTTPClient.
func (m *meteredAWSClient) Do(req *http.Request) (*http.Response, error) {
	return m.rt.RoundTrip(req)
}

// unwrap returns the SDK client doing the work.
func (m *meteredAWSClient) unwrap() aws.HTTPClient { return m.inner }

// awsDoTransport adapts an aws.HTTPClient to http.RoundTripper so the
// shared pulse meter can wrap it.
type awsDoTransport struct{ c aws.HTTPClient }

func (d awsDoTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return d.c.Do(req)
}

// meterAWSHTTPClient wraps c in the dashboard meter. It is idempotent and
// leaves a nil client nil, so the SDK keeps its own default.
func meterAWSHTTPClient(c aws.HTTPClient) aws.HTTPClient {
	if c == nil {
		return nil
	}
	if _, already := c.(*meteredAWSClient); already {
		return c
	}
	return &meteredAWSClient{inner: c, rt: pulse.MeterTransport(awsDoTransport{c: c})}
}

// meterRuntimeClient is the bedrockruntime option that meters the client.
func meterRuntimeClient(o *bedrockruntime.Options) {
	o.HTTPClient = meterAWSHTTPClient(o.HTTPClient)
}

// meterControlClient is the bedrock control-plane option that meters the
// client.
func meterControlClient(o *bedrocksvc.Options) {
	o.HTTPClient = meterAWSHTTPClient(o.HTTPClient)
}
