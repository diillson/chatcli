package imagegen

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"go.uber.org/zap"
)

// pngInput is a tiny synthetic input image.
var pngInput = Image{Data: []byte("\x89PNG\r\n\x1a\nfake"), Mime: "image/png", Ext: "png"}

// recorder is a local stand-in for one provider account. It counts every
// request so a test can prove which account an edit reached.
type recorder struct {
	srv  *httptest.Server
	hits atomic.Int32
	last struct {
		path, contentType string
		body              []byte
	}
}

// newXAIRecorder answers /images/edits the way xAI does — a URL to the
// result, served by the same server under /files/.
func newXAIRecorder(t *testing.T) *recorder {
	t.Helper()
	rec := &recorder{}
	rec.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/files/") {
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("EDITEDJPEG"))
			return
		}
		rec.hits.Add(1)
		rec.last.path = r.URL.Path
		rec.last.contentType = r.Header.Get("Content-Type")
		rec.last.body, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]string{{"url": rec.srv.URL + "/files/out.jpg"}},
		})
	}))
	t.Cleanup(rec.srv.Close)
	return rec
}

// newOpenAIRecorder answers like OpenAI Images (b64_json).
func newOpenAIRecorder(t *testing.T) *recorder {
	t.Helper()
	rec := &recorder{}
	rec.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.hits.Add(1)
		rec.last.path = r.URL.Path
		rec.last.contentType = r.Header.Get("Content-Type")
		rec.last.body, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString([]byte("OPENAIPNG"))}},
		})
	}))
	t.Cleanup(rec.srv.Close)
	return rec
}

// pointCloudsAt redirects the OpenAI and xAI endpoints to local recorders.
func pointCloudsAt(t *testing.T, openai, xai *recorder) {
	t.Helper()
	prevOpenAI, prevXAI := openAIBaseURL, xaiBaseURL
	openAIBaseURL, xaiBaseURL = openai.srv.URL, xai.srv.URL
	t.Cleanup(func() { openAIBaseURL, xaiBaseURL = prevOpenAI, prevXAI })
}

// clearImageEnv makes the factory see only what a test sets.
func clearImageEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"CHATCLI_IMAGE_PROVIDER", "CHATCLI_IMAGE_MODEL", "CHATCLI_IMAGE_URL", "CHATCLI_IMAGE_API",
		"CHATCLI_IMAGE_EDIT_PROVIDER", "CHATCLI_IMAGE_EDIT_MODEL", "CHATCLI_IMAGE_URL_EDITS",
		"OPENAI_API_KEY", "XAI_API_KEY", "ZAI_API_KEY", "MINIMAX_API_KEY",
		"GOOGLEAI_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY",
	} {
		t.Setenv(k, "")
	}
}

// xAI edits as application/json with the input as a data URI under image.url,
// never sends size, and the URL result is downloaded with the Go client.
func TestXAIEdit_JSONWithDataURI(t *testing.T) {
	xai := newXAIRecorder(t)
	p, err := NewOpenAICompatible(xai.srv.URL, "k", "grok-imagine-image", "xai", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	p.omitSize = true
	p.edit = editJSONImageURL

	imgs, err := p.Edit(context.Background(), "make it winter", []Image{pngInput}, EditOptions{Size: "1024x1024"})
	if err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if xai.last.path != imagesEditPath {
		t.Errorf("path = %q, want %q", xai.last.path, imagesEditPath)
	}
	if xai.last.contentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json (xAI rejects multipart)", xai.last.contentType)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(xai.last.body, &body); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, xai.last.body)
	}
	image, _ := body["image"].(map[string]interface{})
	wantURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngInput.Data)
	if image["url"] != wantURI {
		t.Errorf("image.url = %v, want the input as a data URI", image["url"])
	}
	if body["model"] != "grok-imagine-image" || body["prompt"] != "make it winter" {
		t.Errorf("model/prompt = %v/%v", body["model"], body["prompt"])
	}
	if _, has := body["size"]; has {
		t.Error("size must not be sent to xAI")
	}
	if len(imgs) != 1 || string(imgs[0].Data) != "EDITEDJPEG" || imgs[0].Ext != "jpg" {
		t.Errorf("result = %+v, want the downloaded JPEG", imgs)
	}
}

func TestXAIEdit_SeveralInputsGoAsImages(t *testing.T) {
	xai := newXAIRecorder(t)
	p, _ := NewOpenAICompatible(xai.srv.URL, "k", "grok-imagine-image", "xai", zap.NewNop())
	p.edit = editJSONImageURL
	second := Image{Data: []byte("second"), Mime: "image/jpeg", Ext: "jpg"}
	if _, err := p.Edit(context.Background(), "merge", []Image{pngInput, second}, EditOptions{}); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Image  interface{}         `json:"image"`
		Images []map[string]string `json:"images"`
	}
	_ = json.Unmarshal(xai.last.body, &body)
	if body.Image != nil || len(body.Images) != 2 || !strings.HasPrefix(body.Images[1]["url"], "data:image/jpeg;base64,") {
		t.Fatalf("several inputs must ride as images[], got %s", xai.last.body)
	}
}

// The reported failure: xAI active, an OpenAI key also present. The edit must
// reach xAI and nothing must reach OpenAI.
func TestImageEdit_ActiveXAIEditsAndNeverCallsOpenAI(t *testing.T) {
	clearImageEnv(t)
	openai, xai := newOpenAIRecorder(t), newXAIRecorder(t)
	pointCloudsAt(t, openai, xai)
	t.Setenv("CHATCLI_IMAGE_MODEL", "grok-imagine-image")
	t.Setenv("XAI_API_KEY", "xai-key")
	t.Setenv("OPENAI_API_KEY", "openai-key")

	active := NewFromEnv(zap.NewNop())
	ed, used, routed, ok := ResolveEditor(context.Background(), active, zap.NewNop())
	if !ok || used != "xai" || routed {
		t.Fatalf("ResolveEditor = used %q routed %v ok %v, want the active xai", used, routed, ok)
	}
	if _, err := ed.Edit(context.Background(), "make it winter", []Image{pngInput}, EditOptions{}); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if xai.hits.Load() != 1 {
		t.Errorf("xAI hits = %d, want 1", xai.hits.Load())
	}
	if openai.hits.Load() != 0 {
		t.Errorf("OpenAI hits = %d, want 0 — the edit leaked to another account", openai.hits.Load())
	}
}

// A backend with no edit endpoint fails before any request, even when another
// account could edit.
func TestImageEdit_GenerateOnlyBackendFailsWithoutCrossRequest(t *testing.T) {
	clearImageEnv(t)
	openai, xai := newOpenAIRecorder(t), newXAIRecorder(t)
	pointCloudsAt(t, openai, xai)
	t.Setenv("CHATCLI_IMAGE_PROVIDER", "zai")
	t.Setenv("ZAI_API_KEY", "zai-key")
	t.Setenv("OPENAI_API_KEY", "openai-key")
	t.Setenv("XAI_API_KEY", "xai-key")

	active := NewFromEnv(zap.NewNop())
	if _, _, _, ok := ResolveEditor(context.Background(), active, zap.NewNop()); ok {
		t.Fatal("zai must not resolve an editor")
	}
	if ue := EditUnavailable(active); ue.Backend != "zai" || ue.Model != defaultZAIImageModel {
		t.Fatalf("EditUnavailable = %+v, want zai/%s", ue, defaultZAIImageModel)
	}
	if n := openai.hits.Load() + xai.hits.Load(); n != 0 {
		t.Fatalf("%d request(s) reached another account", n)
	}
	// A caller that skips AsEditor still gets an explicit error, no request.
	z := zaiOrNull("", zap.NewNop(), "")
	if _, err := z.(*OpenAICompatible).Edit(context.Background(), "x", []Image{pngInput}, EditOptions{}); !errors.Is(err, ErrEditUnsupported) {
		t.Fatalf("zai Edit err = %v, want ErrEditUnsupported", err)
	}
}

// With the pin, the edit goes to the pinned backend, and only there.
func TestImageEdit_PinnedBackendIsTheOnlyRoute(t *testing.T) {
	clearImageEnv(t)
	openai, xai := newOpenAIRecorder(t), newXAIRecorder(t)
	pointCloudsAt(t, openai, xai)
	t.Setenv("CHATCLI_IMAGE_PROVIDER", "zai")
	t.Setenv("ZAI_API_KEY", "zai-key")
	t.Setenv("XAI_API_KEY", "xai-key")
	t.Setenv("OPENAI_API_KEY", "openai-key")
	t.Setenv("CHATCLI_IMAGE_EDIT_PROVIDER", "xai")

	ed, used, routed, ok := ResolveEditor(context.Background(), NewFromEnv(zap.NewNop()), zap.NewNop())
	if !ok || used != "xai" || !routed {
		t.Fatalf("ResolveEditor = used %q routed %v ok %v, want the pinned xai", used, routed, ok)
	}
	if _, err := ed.Edit(context.Background(), "x", []Image{pngInput}, EditOptions{}); err != nil {
		t.Fatal(err)
	}
	if xai.hits.Load() != 1 || openai.hits.Load() != 0 {
		t.Fatalf("hits xai=%d openai=%d, want 1/0", xai.hits.Load(), openai.hits.Load())
	}
}

// OpenAI Images keeps its multipart edit.
func TestOpenAIEdit_StaysMultipart(t *testing.T) {
	openai := newOpenAIRecorder(t)
	p := cloudOrNull(openai.srv.URL, "k", "gpt-image-2", "openai", zap.NewNop(), "")
	ed, ok := AsEditor(p)
	if !ok {
		t.Fatal("openai must edit")
	}
	if _, err := ed.Edit(context.Background(), "x", []Image{pngInput}, EditOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(openai.last.contentType, "multipart/form-data") {
		t.Fatalf("Content-Type = %q, want multipart/form-data", openai.last.contentType)
	}
}

// The Responses API edits in the /responses call itself: the input image is
// an input_image part and the tool is forced to the edit action.
func TestOpenAIResponsesEdit_InputImageAndEditAction(t *testing.T) {
	var gotPath string
	var got struct {
		Input []struct {
			Role    string                   `json:"role"`
			Content []map[string]interface{} `json:"content"`
		} `json:"input"`
		Tools []map[string]interface{} `json:"tools"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"output": []map[string]string{{"type": "image_generation_call", "result": base64.StdEncoding.EncodeToString([]byte("RESP"))}},
		})
	}))
	defer srv.Close()

	p, err := NewOpenAIResponses(srv.URL, "k", "gpt-5.6-sol", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	ed, ok := AsEditor(p)
	if !ok {
		t.Fatal("the Responses backend must edit")
	}
	imgs, err := ed.Edit(context.Background(), "make it winter", []Image{pngInput}, EditOptions{Mask: []byte("m")})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != responsesPath || len(imgs) != 1 || string(imgs[0].Data) != "RESP" {
		t.Fatalf("path %q, images %+v", gotPath, imgs)
	}
	if len(got.Input) != 1 || len(got.Input[0].Content) != 2 {
		t.Fatalf("input = %+v, want one user message with text + image", got.Input)
	}
	part := got.Input[0].Content[1]
	if part["type"] != "input_image" || !strings.HasPrefix(part["image_url"].(string), "data:image/png;base64,") {
		t.Errorf("image part = %v", part)
	}
	tool := got.Tools[0]
	if tool["type"] != "image_generation" || tool["action"] != "edit" {
		t.Errorf("tool = %v, want image_generation with action edit", tool)
	}
	if mask, _ := tool["input_image_mask"].(map[string]interface{}); mask == nil {
		t.Error("mask not sent as input_image_mask")
	}
}

// Bedrock edits per model: SD3.5 Large and Stable Image Ultra do, Stable Image
// Core (the default) does not.
func TestBedrock_EditIsPerModel(t *testing.T) {
	cases := map[string]bool{
		"stability.sd3-5-large-v1:0":        true,
		"us.stability.sd3-5-large-v1:0":     true,
		"stability.stable-image-ultra-v1:1": true,
		"amazon.nova-canvas-v1:0":           true,
		defaultBedrockImageModel:            false,
		"stability.some-future-model-v9:0":  false,
	}
	for model, want := range cases {
		b := &Bedrock{model: model}
		if _, ok := AsEditor(b); ok != want {
			t.Errorf("%s: edits=%v, want %v", model, ok, want)
		}
	}
	core := &Bedrock{model: defaultBedrockImageModel}
	if _, err := core.Edit(context.Background(), "x", []Image{pngInput}, EditOptions{}); !errors.Is(err, ErrEditUnsupported) {
		t.Fatalf("core Edit err = %v, want ErrEditUnsupported", err)
	}

	var ultra map[string]interface{}
	_ = json.Unmarshal(buildBedrockEditRequest("stability.stable-image-ultra-v1:1", "p", []byte("i"), EditOptions{}), &ultra)
	if _, has := ultra["mode"]; has || ultra["image"] == nil || ultra["strength"] == nil {
		t.Errorf("ultra edit body = %v, want image+strength without mode", ultra)
	}
}

func TestSelfHosted_EditsUnlessDeclaredGenerateOnly(t *testing.T) {
	t.Setenv("CHATCLI_IMAGE_KEY", "")
	for v, want := range map[string]bool{"": true, "true": true, "1": true, "false": false, "0": false} {
		t.Setenv("CHATCLI_IMAGE_URL_EDITS", v)
		if _, ok := AsEditor(selfHostedOrNull("http://localhost:8080/v1", "", zap.NewNop())); ok != want {
			t.Errorf("CHATCLI_IMAGE_URL_EDITS=%q: edits=%v, want %v", v, ok, want)
		}
	}
}

// A self-hosted server without /images/edits gets a hint naming the flag.
func TestSelfHosted_MissingEditEndpointNamesTheFlag(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	t.Setenv("CHATCLI_IMAGE_URL_EDITS", "")
	p := selfHostedOrNull(srv.URL, "", zap.NewNop())
	_, err := p.(*OpenAICompatible).Edit(context.Background(), "x", []Image{pngInput}, EditOptions{})
	if err == nil || !strings.Contains(err.Error(), "CHATCLI_IMAGE_URL_EDITS=false") {
		t.Fatalf("err = %v, want a hint naming CHATCLI_IMAGE_URL_EDITS", err)
	}
}

// A URL result that can't be downloaded says why instead of "no decodable
// images".
func TestDecodeImageResponse_ReportsDownloadFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/files/out.jpg" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]string{{"url": "http://" + r.Host + "/files/out.jpg"}},
		})
	}))
	defer srv.Close()
	p, _ := NewOpenAICompatible(srv.URL, "k", "m", "xai", zap.NewNop())
	_, err := p.Generate(context.Background(), "x", Options{})
	if err == nil || !strings.Contains(err.Error(), "download status 403") {
		t.Fatalf("err = %v, want the download status", err)
	}
}
