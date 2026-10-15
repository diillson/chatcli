package imagegen

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

// isolate clears every credential and override the image backends read.
func isolate(t *testing.T) {
	t.Helper()
	for _, k := range []string{"CHATCLI_IMAGE_PROVIDER", "CHATCLI_IMAGE_API", "CHATCLI_IMAGE_MODEL", "CHATCLI_IMAGE_URL",
		"CHATCLI_IMAGE_EDIT_PROVIDER", "CHATCLI_IMAGE_EDIT_MODEL", "OPENAI_API_KEY", "GOOGLEAI_API_KEY", "GEMINI_API_KEY",
		"GOOGLE_API_KEY", "XAI_API_KEY", "ZAI_API_KEY", "MINIMAX_API_KEY", "AWS_PROFILE", "BEDROCK_PROFILE",
		"AWS_ACCESS_KEY_ID", "AWS_BEARER_TOKEN_BEDROCK", "AWS_WEB_IDENTITY_TOKEN_FILE",
		"AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "AWS_REGION", "BEDROCK_REGION", "AWS_DEFAULT_REGION"} {
		t.Setenv(k, "")
	}
	t.Setenv("AWS_CONFIG_FILE", "/nonexistent/aws-config")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/nonexistent/aws-credentials")
}

func atDate(t *testing.T, day string) {
	t.Helper()
	d, err := time.Parse("2006-01-02", day)
	if err != nil {
		t.Fatal(err)
	}
	prev := catalogNow
	catalogNow = func() time.Time { return d }
	t.Cleanup(func() { catalogNow = prev })
}

func names(ms []ModelInfo) map[string]bool {
	out := map[string]bool{}
	for _, m := range ms {
		out[m.Name] = true
	}
	return out
}

// A model leaves the list on its shutdown day, without a release.
func TestKnownModels_DropsModelsOnTheirShutdownDay(t *testing.T) {
	atDate(t, "2026-10-22")
	if !names(KnownModels())["gpt-image-1"] {
		t.Fatal("gpt-image-1 is still in service the day before its shutdown")
	}
	atDate(t, "2026-10-23")
	got := names(KnownModels())
	if got["gpt-image-1"] {
		t.Fatal("gpt-image-1 must leave the list on 2026-10-23")
	}
	if got["amazon.nova-canvas-v1:0"] || got["gemini-3-pro-image-preview"] {
		t.Fatal("models past their end of life must not be listed")
	}
	if _, retired, ok := LookupModel("gpt-image-1"); !ok || !retired {
		t.Fatal("LookupModel must still recognize a retired id")
	}
}

func TestCatalog_OneDefaultPerBackendAndConsistentFlags(t *testing.T) {
	defaults := map[string]int{}
	for _, m := range allModels() {
		if m.Default {
			defaults[m.Provider+"/"+m.API]++
		}
		if !m.Generate && !m.Edit {
			t.Errorf("%s neither generates nor edits", m.Name)
		}
		if m.Provider == "bedrock" && m.Retires == "" && len(m.Regions) == 0 {
			t.Errorf("%s: a Bedrock model needs its regions", m.Name)
		}
	}
	for k, n := range defaults {
		if n != 1 {
			t.Errorf("%s has %d defaults", k, n)
		}
	}
	if DefaultModelFor("google", "") != "gemini-nano-banana-2.1" || DefaultModelFor("openai", "images") != "gpt-image-2.5-flare" ||
		DefaultModelFor("openai", "responses") != "gpt-5.6-sol" || DefaultModelFor("bedrock", "") != defaultBedrockImageModel {
		t.Fatal("unexpected defaults")
	}
}

func TestBackendForModel(t *testing.T) {
	cases := map[string]string{
		"gpt-image-2.5-sunburst":                 "openai",
		"gpt-image-2-2026-04-21":                 "openai",
		"chatgpt-image-latest":                   "openai",
		"gpt-5.6-sol":                            "responses",
		"gemini-nano-banana-2.1":                 "google",
		"nano-banana-pro-preview":                "google",
		"grok-imagine-image-quality":             "xai",
		"glm-image":                              "zai",
		"image-01":                               "minimax",
		"stability.stable-image-core-v1:1":       "bedrock",
		"us.stability.stable-image-inpaint-v1:0": "bedrock",
		"unknown-model":                          "",
	}
	for model, want := range cases {
		if got := BackendForModel(model); got != want {
			t.Errorf("BackendForModel(%q) = %q, want %q", model, got, want)
		}
	}
	if !isGeminiModel("nano-banana-pro-preview") || isGeminiModel("imagen-4.0-generate-001") {
		t.Error("only imagen-* models go to :predict")
	}
}

// The reported failure: Core/Ultra/SD3.5 exist only in us-west-2, so the
// configured chat region must not be used for them.
func TestBedrock_RegionAndProfilePerModel(t *testing.T) {
	cases := []struct {
		model, configured, wantRegion, wantInvoke string
	}{
		{"stability.stable-image-core-v1:1", "us-east-1", "us-west-2", "stability.stable-image-core-v1:1"},
		{"stability.stable-image-core-v1:1", "", "us-west-2", "stability.stable-image-core-v1:1"},
		{"stability.sd3-5-large-v1:0", "us-west-2", "us-west-2", "stability.sd3-5-large-v1:0"},
		{"stability.stable-image-inpaint-v1:0", "us-west-2", "us-west-2", "us.stability.stable-image-inpaint-v1:0"},
		{"stability.stable-image-inpaint-v1:0", "sa-east-1", "us-east-1", "us.stability.stable-image-inpaint-v1:0"},
		{"us.stability.stable-image-remove-background-v1:0", "us-east-1", "us-east-1", "us.stability.stable-image-remove-background-v1:0"},
		{"some.future-model-v1:0", "eu-west-1", "eu-west-1", "some.future-model-v1:0"},
	}
	for _, c := range cases {
		region := bedrockRegionFor(c.model, c.configured)
		if region != c.wantRegion {
			t.Errorf("%s from %q: region %q, want %q", c.model, c.configured, region, c.wantRegion)
		}
		if got := bedrockInvokeID(c.model, region); got != c.wantInvoke {
			t.Errorf("%s in %s: invoke %q, want %q", c.model, region, got, c.wantInvoke)
		}
	}
}

func TestBedrock_GenerationAndEditPerModel(t *testing.T) {
	if !bedrockEditOnly("stability.stable-image-inpaint-v1:0") || bedrockEditOnly("stability.sd3-5-large-v1:0") || bedrockEditOnly(defaultBedrockImageModel) {
		t.Fatal("only the Image Services need an input image")
	}
	b := &Bedrock{model: "stability.stable-image-remove-background-v1:0"}
	if _, err := b.Generate(context.Background(), "x", Options{}); err == nil || !strings.Contains(err.Error(), "@image edit") {
		t.Fatalf("generating with an edit-only model must say to use edit, got %v", err)
	}
	img, mask := []byte("img"), []byte("mask")
	bodies := map[string]map[string]interface{}{}
	for _, model := range []string{
		"stability.stable-image-inpaint-v1:0", "stability.stable-image-erase-object-v1:0",
		"stability.stable-image-remove-background-v1:0", "stability.stable-image-control-sketch-v1:0",
		"stability.stable-image-style-guide-v1:0", "stability.stable-creative-upscale-v1:0",
		"stability.stable-fast-upscale-v1:0",
	} {
		var m map[string]interface{}
		_ = json.Unmarshal(buildBedrockEditRequest(model, "make it blue", img, EditOptions{Mask: mask, Strength: 0.3}), &m)
		bodies[model] = m
		if m["image"] == nil {
			t.Errorf("%s: no image", model)
		}
	}
	if bodies["stability.stable-image-inpaint-v1:0"]["mask"] == nil || bodies["stability.stable-image-inpaint-v1:0"]["prompt"] != "make it blue" {
		t.Error("inpaint takes prompt and mask")
	}
	if bodies["stability.stable-image-erase-object-v1:0"]["prompt"] != nil || bodies["stability.stable-image-erase-object-v1:0"]["mask"] == nil {
		t.Error("erase-object takes a mask and no prompt")
	}
	if _, has := bodies["stability.stable-image-remove-background-v1:0"]["prompt"]; has {
		t.Error("remove-background takes no prompt")
	}
	if bodies["stability.stable-image-control-sketch-v1:0"]["control_strength"] != 0.3 {
		t.Error("control-sketch maps strength to control_strength")
	}
	if bodies["stability.stable-creative-upscale-v1:0"]["creativity"] != 0.3 {
		t.Error("creative upscale maps strength to creativity")
	}
	// Generation bodies carry no "mode": only SD3.5 documents it.
	var gen map[string]interface{}
	_ = json.Unmarshal(buildBedrockRequest(defaultBedrockImageModel, "x", Options{}), &gen)
	if _, has := gen["mode"]; has {
		t.Error("text-to-image body must not send mode")
	}
}

func TestBedrock_ExplainsRegionAndProfileErrors(t *testing.T) {
	err := explainBedrockInvokeError("stability.stable-image-core-v1:1", "us-east-1",
		errors.New("ValidationException: The provided model identifier is invalid."))
	if !strings.Contains(err.Error(), "us-west-2") || !strings.Contains(err.Error(), "us-east-1") {
		t.Fatalf("invalid-id error must name where the model is offered: %v", err)
	}
	err = explainBedrockInvokeError("stability.stable-image-inpaint-v1:0", "us-east-1",
		errors.New("ValidationException: Invocation of model ID stability.stable-image-inpaint-v1:0 with on-demand throughput isn't supported."))
	if !strings.Contains(err.Error(), "us.stability.stable-image-inpaint-v1:0") {
		t.Fatalf("on-demand error must name the profile id: %v", err)
	}
}

func TestLiveListing_XAIAndGoogle(t *testing.T) {
	isolate(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/image-generation-models"):
			if r.Header.Get("Authorization") != "Bearer xk" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"models":[{"id":"grok-imagine-image-2.0"},{"id":"grok-imagine-image-ultra"}]}`))
		case strings.HasSuffix(r.URL.Path, "/v1beta/models"):
			if r.Header.Get("x-goog-api-key") != "gk" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_, _ = w.Write([]byte(`{"models":[{"name":"models/gemini-nano-banana-2.1"},{"name":"models/gemini-3-pro-image-preview"},{"name":"models/gemini-3.5-flash"},{"name":"models/nano-banana-pro-preview"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	prevX, prevG := xaiImageModelsURL, googleModelsURL
	xaiImageModelsURL, googleModelsURL = srv.URL+"/v1/image-generation-models", srv.URL+"/v1beta/models"
	t.Cleanup(func() { xaiImageModelsURL, googleModelsURL = prevX, prevG })
	t.Setenv("XAI_API_KEY", "xk")
	t.Setenv("GOOGLEAI_API_KEY", "gk")

	listings := ListModels(context.Background(), "", "xai", true, zap.NewNop())
	byBackend := map[string]BackendListing{}
	for _, l := range listings {
		byBackend[l.Name] = l
	}
	x := byBackend["xai"]
	if !x.Active || !x.LiveChecked {
		t.Fatalf("xai listing = %+v", x)
	}
	models := map[string]ListedModel{}
	for _, m := range x.Models {
		models[m.Name] = m
	}
	if !models["grok-imagine-image-2.0"].Live || models["grok-imagine-image"].Live {
		t.Error("live flags must follow the listing")
	}
	if nm := models["grok-imagine-image-ultra"]; !nm.Live || !nm.Supported || nm.Note == "" {
		t.Errorf("a listed model the catalog lacks must appear: %+v", nm)
	}
	g := map[string]ListedModel{}
	for _, m := range byBackend["google"].Models {
		g[m.Name] = m
	}
	if !g["gemini-nano-banana-2.1"].Live || !g["nano-banana-pro-preview"].Live {
		t.Error("google image models (including nano-banana ids) must be listed live")
	}
	if _, listed := g["gemini-3-pro-image-preview"]; listed {
		t.Error("a retired id the provider still lists must stay out")
	}
	if _, listed := g["gemini-3.5-flash"]; listed {
		t.Error("text models must not be listed")
	}
	if byBackend["zai"].Configured || byBackend["zai"].LiveChecked {
		t.Error("an unconfigured backend is not listed live")
	}
}

func TestSelect(t *testing.T) {
	isolate(t)
	t.Setenv("GOOGLEAI_API_KEY", "gk")
	t.Setenv("CHATCLI_IMAGE_API", "responses")

	sel, err := Select("", "gemini-nano-banana-2.1", false)
	if err != nil || sel.Backend != "google" {
		t.Fatalf("Select = %+v, %v", sel, err)
	}
	sel.Apply()
	if got := NewFromEnv(zap.NewNop()); got.Name() != "google" || ModelOf(got) != "gemini-nano-banana-2.1" {
		t.Fatalf("after Apply the factory builds %s/%s", got.Name(), ModelOf(got))
	}
	if v := envOrEmpty("CHATCLI_IMAGE_API"); v != "" {
		t.Fatalf("CHATCLI_IMAGE_API must be cleared, got %q", v)
	}

	// Provider only: its default model.
	if sel, _ := Select("gemini", "", false); sel.Model != "gemini-nano-banana-2.1" {
		t.Fatalf("default model = %q", sel.Model)
	}
	// Not configured.
	if _, err := Select("", "grok-imagine-image-2.0", false); err == nil || !strings.Contains(err.Error(), "XAI_API_KEY") {
		t.Fatalf("an unconfigured provider must name its credential, got %v", err)
	}
	// Retired.
	t.Setenv("OPENAI_API_KEY", "ok")
	atDate(t, "2026-10-24")
	if _, err := Select("", "gpt-image-1", false); err == nil || !strings.Contains(err.Error(), "2026-10-23") {
		t.Fatalf("a retired model must be refused with its date, got %v", err)
	}
	// Edit pin.
	sel, err = Select("openai", "", true)
	if err != nil || sel.Env["CHATCLI_IMAGE_EDIT_PROVIDER"] != "openai" || sel.Env["CHATCLI_IMAGE_EDIT_MODEL"] != "gpt-image-2.5-flare" {
		t.Fatalf("edit pin = %+v, %v", sel, err)
	}
	if _, err := Select("", "glm-image", true); err == nil {
		t.Fatal("a model that cannot edit cannot be the edit backend")
	}
	// Edit-only model for generation: allowed, with a warning.
	t.Setenv("AWS_PROFILE", "p")
	sel, err = Select("", "stability.stable-image-inpaint-v1:0", false)
	if err != nil || len(sel.Warnings) == 0 {
		t.Fatalf("an edit-only model for generation must warn: %+v %v", sel, err)
	}
	if _, err := Select("nope", "", false); err == nil {
		t.Fatal("unknown provider")
	}
}

func envOrEmpty(k string) string { return os.Getenv(k) }
