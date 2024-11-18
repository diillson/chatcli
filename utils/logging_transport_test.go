package utils

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestLoggingTransport_RoundTrip(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	transport := &LoggingTransport{
		Logger:      logger,
		Transport:   http.DefaultTransport,
		MaxBodySize: 1024,
	}

	client := &http.Client{Transport: transport}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(w, r.Body)
	}))
	defer server.Close()

	resp, err := client.Post(server.URL, "application/json", strings.NewReader(`{"key":"value"}`))
	if err != nil {
		t.Fatalf("Erro na requisição: %v", err)
	}
	resp.Body.Close()
}
