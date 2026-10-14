package imagegen

import (
	"context"
	"encoding/base64"
	"errors"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// TestAutomatic1111_Edit drives the SD WebUI img2img path: the input image is
// posted as init_images and the base64 result is decoded.
func TestAutomatic1111_Edit(t *testing.T) {
	var gotPath, gotInit string
	var gotDenoise float64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		var req struct {
			InitImages        []string `json:"init_images"`
			DenoisingStrength float64  `json:"denoising_strength"`
		}
		_ = json.Unmarshal(body, &req)
		if len(req.InitImages) > 0 {
			gotInit = req.InitImages[0]
		}
		gotDenoise = req.DenoisingStrength
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"images": []string{base64.StdEncoding.EncodeToString([]byte("EDITEDPNG"))},
		})
	}))
	defer srv.Close()

	p, err := NewAutomatic1111(srv.URL, 20, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	imgs, err := p.Edit(context.Background(), "make it winter",
		[]Image{{Data: []byte("inputbytes"), Mime: "image/png", Ext: "png"}},
		EditOptions{Strength: 0.4})
	if err != nil {
		t.Fatalf("Edit failed: %v", err)
	}
	if !strings.HasSuffix(gotPath, sdImg2ImgPath) {
		t.Errorf("expected img2img endpoint, got %q", gotPath)
	}
	if gotInit != base64.StdEncoding.EncodeToString([]byte("inputbytes")) {
		t.Error("init image was not sent as base64")
	}
	if gotDenoise != 0.4 {
		t.Errorf("denoising_strength = %v, want 0.4", gotDenoise)
	}
	if len(imgs) != 1 || string(imgs[0].Data) != "EDITEDPNG" {
		t.Errorf("unexpected result: %+v", imgs)
	}
}

func TestAutomatic1111_Edit_RequiresInput(t *testing.T) {
	p, _ := NewAutomatic1111("http://localhost:7860", 20, zap.NewNop())
	if _, err := p.Edit(context.Background(), "x", nil, EditOptions{}); err == nil {
		t.Error("expected error when no input image")
	}
	if _, err := p.Edit(context.Background(), "", []Image{{Data: []byte("x"), Mime: "image/png"}}, EditOptions{}); err == nil {
		t.Error("expected error on empty prompt")
	}
}

func TestBuildBedrockEditRequest(t *testing.T) {
	img := []byte("imgbytes")
	// Stability → image-to-image mode.
	body := buildBedrockEditRequest("stability.sd3-5-large-v1:0", "winter", img, EditOptions{Strength: 0.5})
	var st map[string]interface{}
	_ = json.Unmarshal(body, &st)
	if st["mode"] != "image-to-image" {
		t.Errorf("stability mode = %v, want image-to-image", st["mode"])
	}
	if st["image"] != base64.StdEncoding.EncodeToString(img) {
		t.Error("stability image not base64-encoded")
	}
	if st["strength"].(float64) != 0.5 {
		t.Errorf("strength = %v, want 0.5", st["strength"])
	}

	// Amazon Nova / Titan → IMAGE_VARIATION task.
	body = buildBedrockEditRequest("amazon.nova-canvas-v1:0", "winter", img, EditOptions{})
	var nv map[string]interface{}
	_ = json.Unmarshal(body, &nv)
	if nv["taskType"] != "IMAGE_VARIATION" {
		t.Errorf("amazon taskType = %v, want IMAGE_VARIATION", nv["taskType"])
	}
	params := nv["imageVariationParams"].(map[string]interface{})
	if params["text"] != "winter" {
		t.Error("variation text not set")
	}
	if imgs := params["images"].([]interface{}); len(imgs) != 1 {
		t.Error("variation images not set")
	}
}

func TestPartContentType(t *testing.T) {
	cases := map[string]Image{
		"image/jpeg": {Mime: "image/jpeg"},
		"image/webp": {Ext: "webp"},
		"image/gif":  {Ext: "gif"},
		"image/png":  {Ext: "unknown"},
	}
	for want, in := range cases {
		if got := partContentType(in); got != want {
			t.Errorf("partContentType(%+v) = %q, want %q", in, got, want)
		}
	}
	// jpg extension folds to image/jpeg.
	if got := partContentType(Image{Ext: "jpg"}); got != "image/jpeg" {
		t.Errorf("jpg ext = %q, want image/jpeg", got)
	}
}

// genOnly is a Provider with no Edit method (generation-only), used to drive
// ResolveEditor when the active backend can't edit.
type genOnly struct{}

func (genOnly) Name() string { return "genonly" }
func (genOnly) Generate(context.Context, string, Options) ([]Image, error) {
	return []Image{{Data: []byte("x"), Mime: "image/png", Ext: "png"}}, nil
}

func TestResolveEditor_PrimaryEdits(t *testing.T) {
	// OpenAI proper is edit-capable, so it is returned as-is (no routing).
	p := cloudOrNull("https://api.openai.com/v1", "k", "gpt-image-2", "openai", zap.NewNop(), "")
	ed, used, routed, err := ResolveEditor(context.Background(), p, zap.NewNop())
	if err != nil || ed == nil || routed || used != "openai" {
		t.Fatalf("expected primary editor openai, got used=%q routed=%v err=%v", used, routed, err)
	}
}

func TestResolveEditor_RoutesOnlyToThePinnedBackend(t *testing.T) {
	t.Setenv("CHATCLI_IMAGE_EDIT_PROVIDER", "sdwebui")
	t.Setenv("CHATCLI_IMAGE_URL", "http://localhost:7860")
	ed, used, routed, err := ResolveEditor(context.Background(), genOnly{}, zap.NewNop())
	if err != nil || ed == nil || !routed || used != "sdwebui" {
		t.Fatalf("expected pinned sdwebui, got used=%q routed=%v err=%v", used, routed, err)
	}
}

// Credentials for other editing backends must not pull an edit away from
// the active backend: without a pin, a backend that can't edit is an error.
func TestResolveEditor_NeverRoutesImplicitly(t *testing.T) {
	t.Setenv("CHATCLI_IMAGE_EDIT_PROVIDER", "")
	t.Setenv("CHATCLI_IMAGE_URL", "http://localhost:7860")
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("GEMINI_API_KEY", "k")
	t.Setenv("AWS_REGION", "us-east-1")
	_, _, _, err := ResolveEditor(context.Background(), genOnly{}, zap.NewNop())
	var ue *EditUnavailableError
	if !errors.As(err, &ue) || ue.Backend != "genonly" || ue.Pinned != "" {
		t.Fatalf("expected EditUnavailableError for genonly, got %v", err)
	}
	if !errors.Is(err, ErrEditUnsupported) {
		t.Error("EditUnavailableError must match ErrEditUnsupported")
	}
}

func TestResolveEditor_PinnedBackendThatCannotEdit(t *testing.T) {
	t.Setenv("CHATCLI_IMAGE_EDIT_PROVIDER", "zai")
	t.Setenv("ZAI_API_KEY", "k")
	_, _, _, err := ResolveEditor(context.Background(), genOnly{}, zap.NewNop())
	var ue *EditUnavailableError
	if !errors.As(err, &ue) || ue.Pinned != "zai" {
		t.Fatalf("expected EditUnavailableError naming the pin, got %v", err)
	}

	t.Setenv("CHATCLI_IMAGE_EDIT_PROVIDER", "openai")
	t.Setenv("OPENAI_API_KEY", "")
	if _, _, _, err := ResolveEditor(context.Background(), genOnly{}, zap.NewNop()); !errors.As(err, &ue) {
		t.Fatalf("a pin without credentials must not resolve, got %v", err)
	}
}

func TestBuildEditProvider_OpenAIAndGoogle(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "k")
	if p := buildEditProvider(context.Background(), "openai", "", zap.NewNop()); p == nil {
		t.Error("openai edit provider should build with a key")
	} else if _, ok := AsEditor(p); !ok {
		t.Error("openai built provider should be an editor")
	}
	t.Setenv("GEMINI_API_KEY", "k")
	if p := buildEditProvider(context.Background(), "google", "", zap.NewNop()); p == nil {
		t.Error("google edit provider should build with a key")
	}
	t.Setenv("XAI_API_KEY", "k")
	if p := buildEditProvider(context.Background(), "xai", "", zap.NewNop()); p == nil {
		t.Error("xai edit provider should build with a key")
	} else if _, ok := AsEditor(p); !ok {
		t.Error("xai built provider should be an editor")
	}
	if p := buildEditProvider(context.Background(), "bogus", "", zap.NewNop()); p != nil {
		t.Error("unknown provider name should return nil")
	}
}
