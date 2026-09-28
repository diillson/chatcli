/*
 * ChatCLI - Kubernetes Operator
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package rest

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestDevModeEnabled_OneParse(t *testing.T) {
	for value, want := range map[string]bool{
		"":        false,
		"true":    true,
		"TRUE":    true,
		"True":    true,
		" true ":  true,
		"1":       true,
		"t":       true,
		"false":   false,
		"FALSE":   false,
		"0":       false,
		"yes":     false,
		"enabled": false,
	} {
		t.Run(value, func(t *testing.T) {
			t.Setenv(DevModeEnvVar, value)
			if got := DevModeEnabled(); got != want {
				t.Errorf("DevModeEnabled() with %q = %v, want %v", value, got, want)
			}
		})
	}
}

// The middleware and the rate limiter follow the same parse the startup
// log reports: "TRUE" used to be announced as dev mode at startup while
// every request was still rejected with 401.
func TestDevMode_MiddlewareAndLimiterAgreeWithTheParse(t *testing.T) {
	for value, want := range map[string]bool{"TRUE": true, "True": true, "1": true, "false": false, "": false} {
		t.Run(value, func(t *testing.T) {
			t.Setenv(DevModeEnvVar, value)
			api := NewAPIServer(fake.NewClientBuilder().WithScheme(newRestScheme()).Build(), ":0")
			h := api.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, requestFrom("127.0.0.1:1", ""))
			if admitted := rec.Code == http.StatusOK; admitted != want {
				t.Errorf("auth with %q = %d, want admitted=%v", value, rec.Code, want)
			}
			limiter, _ := api.rateLimitBucketFor(requestFrom("127.0.0.1:1", ""))
			if onKeyBudget := limiter == api.keyLimiter; onKeyBudget != want {
				t.Errorf("rate limit with %q on the key budget = %v, want %v", value, onKeyBudget, want)
			}
		})
	}
}
