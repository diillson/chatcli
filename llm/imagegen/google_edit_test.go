package imagegen

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestGoogleEdit_GenerateContentShape proves the Google editor posts the input
// image as inline_data to :generateContent and decodes the inlineData response.
func TestGoogleEdit_GenerateContentShape(t *testing.T) {
	var sawInlineData bool
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		// Confirm an inline_data part is present in the request.
		if strings.Contains(string(raw), "inline_data") {
			sawInlineData = true
		}
		resp := map[string]interface{}{
			"candidates": []map[string]interface{}{{
				"content": map[string]interface{}{
					"parts": []map[string]interface{}{{
						"inlineData": map[string]interface{}{
							"data":     base64.StdEncoding.EncodeToString([]byte("EDITEDPNG")),
							"mimeType": "image/png",
						},
					}},
				},
			}},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	g := &Google{baseURL: srv.URL, apiKey: "k", model: "gemini-2.5-flash-image", client: srv.Client()}
	imgs, err := g.Edit(context.Background(), "make it winter",
		[]Image{{Data: []byte("input"), Mime: "image/jpeg", Ext: "jpg"}}, EditOptions{})
	if err != nil {
		t.Fatalf("Edit failed: %v", err)
	}
	if !sawInlineData {
		t.Error("request did not carry inline_data input image")
	}
	if !strings.HasSuffix(path, ":generateContent") {
		t.Errorf("expected :generateContent endpoint, got %q", path)
	}
	if len(imgs) != 1 || string(imgs[0].Data) != "EDITEDPNG" {
		t.Errorf("unexpected decoded image: %+v", imgs)
	}
}

// TestGoogleEdit_RoutesImagenToGemini proves an Imagen model is rerouted to a
// Gemini image model for editing (Imagen :predict can't edit conversationally).
func TestGoogleEdit_RoutesImagenToGemini(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		resp := map[string]interface{}{
			"candidates": []map[string]interface{}{{
				"content": map[string]interface{}{
					"parts": []map[string]interface{}{{
						"inlineData": map[string]interface{}{"data": base64.StdEncoding.EncodeToString([]byte("X"))},
					}},
				},
			}},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	g := &Google{baseURL: srv.URL, apiKey: "k", model: "imagen-3.0-generate-002", client: srv.Client()}
	if _, err := g.Edit(context.Background(), "x", []Image{{Data: []byte("i"), Mime: "image/png"}}, EditOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(path, defaultGeminiImageModel) {
		t.Errorf("Imagen edit should route to %q, path was %q", defaultGeminiImageModel, path)
	}
}
