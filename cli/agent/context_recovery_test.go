package agent

import (
	"errors"
	"testing"
)

func TestIsProxyWAFRejection(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain 403 auth", errors.New("403 Forbidden: invalid_api_key"), false},
		{"403 + waf keyword", errors.New("403 forbidden — blocked by firewall"), true},
		{"403 + cloudflare", errors.New("HTTP 403: cloudflare challenge"), true},

		// The Bedrock-via-corporate-proxy case that motivated this test.
		{
			name: "bedrock 403 + html body",
			err: errors.New(
				"operation error Bedrock Runtime: InvokeModel, " +
					"https response error StatusCode: 403, RequestID: , " +
					"deserialization failed, failed to decode response body, " +
					"invalid character '<' looking for beginning of value"),
			want: true,
		},
		{
			name: "403 + bare invalid character '<'",
			err:  errors.New("StatusCode: 403, invalid character '<' looking for beginning of value"),
			want: true,
		},

		// Don't flag SDK decode failures that aren't paired with a 403 —
		// those could be a model returning malformed JSON, not a middlebox.
		{"decode failure without 403", errors.New("deserialization failed, invalid character '<'"), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsProxyWAFRejection(tc.err); got != tc.want {
				t.Errorf("IsProxyWAFRejection(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestIsLikelyPayloadProblem_BedrockHTMLBody(t *testing.T) {
	err := errors.New(
		"operation error Bedrock Runtime: InvokeModel, " +
			"https response error StatusCode: 403, RequestID: , " +
			"deserialization failed, invalid character '<' looking for beginning of value")

	if !IsLikelyPayloadProblem(err, 0) {
		t.Fatalf("expected Bedrock HTML-body 403 to be classified as payload problem regardless of history size")
	}
}
