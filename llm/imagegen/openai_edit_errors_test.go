package imagegen

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"
)

func TestOpenAIEdit_Errors(t *testing.T) {
	p, _ := NewOpenAICompatible("https://api.openai.com/v1", "k", "gpt-image-1", "openai", zap.NewNop())

	if _, err := p.Edit(context.Background(), "", []Image{{Data: []byte("x"), Mime: "image/png"}}, EditOptions{}); err == nil {
		t.Error("empty prompt should error")
	}
	if _, err := p.Edit(context.Background(), "x", nil, EditOptions{}); err == nil {
		t.Error("missing input image should error")
	}
}

func TestOpenAIEdit_Non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad"}}`))
	}))
	defer srv.Close()
	p, _ := NewOpenAICompatible(srv.URL, "k", "gpt-image-1", "openai", zap.NewNop())
	if _, err := p.Edit(context.Background(), "x",
		[]Image{{Data: []byte("x"), Mime: "image/png", Ext: "png"}}, EditOptions{}); err == nil {
		t.Error("non-200 response should error")
	}
}
