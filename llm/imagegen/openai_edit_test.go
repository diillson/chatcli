package imagegen

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// TestOpenAIEdit_PartContentType proves the /images/edits multipart upload
// carries the real image MIME on the part header, not the stdlib's default
// application/octet-stream (which OpenAI rejects with "unsupported mimetype").
func TestOpenAIEdit_PartContentType(t *testing.T) {
	var gotImageCT, gotMaskCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, imagesEditPath) {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		mr, err := r.MultipartReader()
		if err != nil {
			t.Fatalf("not multipart: %v", err)
		}
		for {
			part, err := mr.NextPart()
			if err != nil {
				break
			}
			switch part.FormName() {
			case "image":
				gotImageCT = part.Header.Get("Content-Type")
			case "mask":
				gotMaskCT = part.Header.Get("Content-Type")
			}
			_ = part.Close()
		}
		out := map[string]interface{}{
			"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString([]byte("FAKEPNGBYTES"))}},
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()

	p, err := NewOpenAICompatible(srv.URL, "k", "gpt-image-1", "openai", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	in := Image{Data: []byte("not-really-jpeg-but-mime-is-explicit"), Mime: "image/jpeg", Ext: "jpg"}
	if _, err := p.Edit(context.Background(), "make it winter", []Image{in},
		EditOptions{Mask: []byte("maskbytes")}); err != nil {
		t.Fatalf("Edit failed: %v", err)
	}
	if gotImageCT != "image/jpeg" {
		t.Errorf("image part Content-Type = %q, want image/jpeg (NOT application/octet-stream)", gotImageCT)
	}
	if gotMaskCT != "image/png" {
		t.Errorf("mask part Content-Type = %q, want image/png", gotMaskCT)
	}
}
