/*
 * ChatCLI - Tests for vision of off-catalog models
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// routeClient answers per model: an error, or a reply. It records every
// call so a test can tell whether a probe went out.
type routeClient struct {
	model string
	m     *routeManager
}

func (c *routeClient) GetModelName() string { return c.model }
func (c *routeClient) SendPrompt(_ context.Context, _ string, hist []models.Message, maxTokens int) (string, error) {
	c.m.mu.Lock()
	defer c.m.mu.Unlock()
	images := 0
	for _, h := range hist {
		images += len(h.Images)
	}
	c.m.calls = append(c.m.calls, routeCall{model: c.model, images: images, maxTokens: maxTokens})
	if err, ok := c.m.errs[c.model]; ok {
		return "", err
	}
	return "described: a login form", nil
}

type routeCall struct {
	model     string
	images    int
	maxTokens int
}

type routeManager struct {
	*minimalManager
	mu      sync.Mutex
	errs    map[string]error
	calls   []routeCall
	listing map[string][]client.ModelInfo
	listed  []string
}

func (m *routeManager) GetClient(_ string, model string) (client.LLMClient, error) {
	return &routeClient{model: model, m: m}, nil
}

func (m *routeManager) ListModelsForProvider(_ context.Context, provider string) ([]client.ModelInfo, error) {
	m.mu.Lock()
	m.listed = append(m.listed, provider)
	m.mu.Unlock()
	return m.listing[provider], nil
}

func (m *routeManager) callsTo(model string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range m.calls {
		if c.model == model {
			n++
		}
	}
	return n
}

func resetVisionLearning(t *testing.T) {
	t.Helper()
	reset := func() {
		learnedVision = &visionVerdicts{}
		invokableCache = sync.Map{}
		rememberDescriber("", "")
	}
	reset()
	t.Cleanup(reset)
}

const profileARN = "arn:aws:bedrock:us-east-1:123456789012:application-inference-profile/abc123"

func visionTestCLI(t *testing.T, providers ...string) (*ChatCLI, *routeManager) {
	t.Helper()
	resetVisionLearning(t)
	t.Setenv("CHATCLI_VISION_INPUT", "")
	t.Setenv("CHATCLI_VISION_PROVIDER", "")
	t.Setenv("CHATCLI_VISION_MODEL", "")
	m := &routeManager{minimalManager: &minimalManager{providers: providers}, errs: map[string]error{}, listing: map[string][]client.ModelInfo{}}
	return &ChatCLI{Provider: "BEDROCK", Model: profileARN, logger: zap.NewNop(), manager: m, stateRoot: t.TempDir()}, m
}

func TestIsImageInputRejected(t *testing.T) {
	for _, msg := range []string{
		"ValidationException: This model doesn't support the image content block that you provided. Try again with a different model",
		"400 Bad Request: model does not support image input",
		`json: unknown variant "image_url", expected "text"`,
		"Invalid content type. image_url is only supported by certain models.",
	} {
		if !isImageInputRejected(errors.New(msg)) {
			t.Errorf("not recognized as an image rejection: %q", msg)
		}
	}
	for _, msg := range []string{"connection reset by peer", "ThrottlingException: rate exceeded", "AccessDeniedException", "context deadline exceeded"} {
		if isImageInputRejected(errors.New(msg)) {
			t.Errorf("transport/credential error taken as an image rejection: %q", msg)
		}
	}
}

func TestProbePNGDecodes(t *testing.T) {
	if len(probePNG) < 50 || string(probePNG[1:4]) != "PNG" {
		t.Fatalf("probe image is not a PNG: % x", probePNG)
	}
}

func TestOffCatalogModel_ProbedOnceAndRemembered(t *testing.T) {
	c, m := visionTestCLI(t, "BEDROCK")
	if _, known := catalogKnowsVision("BEDROCK", profileARN); known {
		t.Fatal("fixture: the profile ARN must be unknown to the catalog")
	}
	if got := c.resolveVisionModeFor("BEDROCK", profileARN); got != visionDescribe || m.callsTo(profileARN) != 0 {
		t.Fatalf("resolution without an image must not probe: mode %v, calls %d", got, m.callsTo(profileARN))
	}
	img := []models.ImageContent{{MediaType: "image/png", Data: probePNG}}
	out, desc := c.gateImagesForRoute(context.Background(), "BEDROCK", profileARN, img)
	if len(out) != 1 || desc != "" || m.callsTo(profileARN) != 1 {
		t.Fatalf("an accepted probe sends the image natively: out=%d desc=%q calls=%d", len(out), desc, m.callsTo(profileARN))
	}
	if _, _ = c.gateImagesForRoute(context.Background(), "BEDROCK", profileARN, img); m.callsTo(profileARN) != 1 {
		t.Fatal("the verdict was not remembered: probed again")
	}
	// Persisted: a fresh process reads it back without probing.
	learnedVision = &visionVerdicts{}
	if c.resolveVisionModeFor("BEDROCK", profileARN) != visionNative || m.callsTo(profileARN) != 1 {
		t.Fatal("the verdict did not survive a restart")
	}
	if _, err := os.Stat(filepath.Join(c.stateRoot, visionVerdictFile)); err != nil {
		t.Fatal(err)
	}
}

func TestOffCatalogModel_RejectedProbeDescribes(t *testing.T) {
	c, m := visionTestCLI(t, "BEDROCK")
	m.errs[profileARN] = errors.New("ValidationException: This model doesn't support the image content block")
	m.listing["BEDROCK"] = invokableVisionListing(t)
	img := []models.ImageContent{{MediaType: "image/png", Data: probePNG}}
	out, desc := c.gateImagesForRoute(context.Background(), "BEDROCK", profileARN, img)
	if len(out) != 0 || !strings.Contains(desc, "described: a login form") {
		t.Fatalf("a blind model gets the description: out=%d desc=%q", len(out), desc)
	}
	if sees, known := learnedVision.lookup(c.stateRoot, "BEDROCK", profileARN); !known || sees {
		t.Fatal("rejection not remembered as blind")
	}
}

func TestOffCatalogModel_InconclusiveProbeIsNotRemembered(t *testing.T) {
	c, m := visionTestCLI(t, "BEDROCK")
	m.errs[profileARN] = errors.New("ThrottlingException: rate exceeded")
	img := []models.ImageContent{{MediaType: "image/png", Data: probePNG}}
	c.gateImagesForRoute(context.Background(), "BEDROCK", profileARN, img)
	if _, known := learnedVision.lookup(c.stateRoot, "BEDROCK", profileARN); known {
		t.Fatal("an inconclusive probe produced a verdict")
	}
	delete(m.errs, profileARN)
	if out, _ := c.gateImagesForRoute(context.Background(), "BEDROCK", profileARN, img); len(out) != 1 {
		t.Fatal("the next image did not probe again")
	}
}

func TestCatalogKnownModels_NeverProbed(t *testing.T) {
	c, m := visionTestCLI(t, "BEDROCK")
	var blind, sighted string
	for _, meta := range catalog.ListByProvider("BEDROCK") {
		if metaHasVision(meta) && sighted == "" {
			sighted = meta.ID
		}
		if !metaHasVision(meta) && blind == "" && !modelIDImpliesVision(meta.ID) {
			blind = meta.ID
		}
	}
	if blind == "" || sighted == "" {
		t.Skip("catalog has no blind/sighted Bedrock pair")
	}
	img := []models.ImageContent{{MediaType: "image/png", Data: probePNG}}
	if out, _ := c.gateImagesForRoute(context.Background(), "BEDROCK", sighted, img); len(out) != 1 || len(m.calls) != 0 {
		t.Fatalf("catalog vision model must go native without a probe: out=%d calls=%+v", len(out), m.calls)
	}
	// The blind model is described by a vision model (one describe call),
	// never probed itself.
	if _, desc := c.gateImagesForRoute(context.Background(), "BEDROCK", blind, img); desc == "" || m.callsTo(blind) != 0 {
		t.Fatalf("catalog blind model was probed or not described: desc=%q calls=%+v", desc, m.calls)
	}
}

// invokableVisionListing is a live listing whose only vision model is in a
// form the catalog resolves (an inference profile id when one resolves).
func invokableVisionListing(t *testing.T) []client.ModelInfo {
	t.Helper()
	for _, meta := range catalog.ListByProvider("BEDROCK") {
		if !metaHasVision(meta) {
			continue
		}
		id := "us." + meta.ID
		if r, ok := catalog.Resolve("BEDROCK", id); !ok || !metaHasVision(r) {
			id = meta.ID
		}
		return []client.ModelInfo{{ID: id, Source: client.ModelSourceAPI}}
	}
	t.Skip("no Bedrock vision model in the catalog")
	return nil
}

func TestDescribeCandidates_AccountModelsFirstAndNextOnFailure(t *testing.T) {
	c, m := visionTestCLI(t, "BEDROCK")
	listing := invokableVisionListing(t)
	m.listing["BEDROCK"] = listing
	cands := c.describeCandidates(context.Background())
	if len(cands) < 2 || cands[0].model != listing[0].ID {
		t.Fatalf("the account's invokable model must come first: %+v", cands)
	}
	// The first candidate is not enabled in this account: the next one
	// describes the image.
	m.errs[listing[0].ID] = errors.New("AccessDeniedException: You don't have access to the model")
	desc, ok := c.describeImagesFallback(context.Background(), []models.ImageContent{{MediaType: "image/png", Data: probePNG}})
	if !ok || !strings.Contains(desc, "described: a login form") {
		t.Fatalf("fallback stopped at the first failing candidate: %q %v", desc, ok)
	}
	// The describer that worked is tried first next time.
	if next := c.describeCandidates(context.Background()); next[0].model == listing[0].ID {
		t.Fatalf("the working describer was not remembered: %+v", next[:2])
	}
}

func TestDescribeRejectedImages_RewritesAndResendsOnce(t *testing.T) {
	c, m := visionTestCLI(t, "BEDROCK")
	m.listing["BEDROCK"] = invokableVisionListing(t)
	img := models.ImageContent{MediaType: "image/png", Data: probePNG}
	c.history = []models.Message{{Role: "user", Content: "look", Images: []models.ImageContent{img}}}
	turn := append([]models.Message(nil), c.history...)
	rejected := errors.New("ValidationException: This model doesn't support the image content block")

	if c.describeRejectedImages(context.Background(), errors.New("connection reset"), "BEDROCK", profileARN, &turn) {
		t.Fatal("a transport error is not an image rejection")
	}
	if !c.describeRejectedImages(context.Background(), rejected, "BEDROCK", profileARN, &turn) {
		t.Fatal("image rejection not recovered")
	}
	for _, h := range [][]models.Message{c.history, turn} {
		if len(h[0].Images) != 0 || !strings.Contains(h[0].Content, "described: a login form") {
			t.Fatalf("images not rewritten into text: %+v", h[0])
		}
	}
	if m.callsTo(profileARN) != 0 {
		t.Fatal("the blind model was asked to describe")
	}
	if sees, known := learnedVision.lookup(c.stateRoot, "BEDROCK", profileARN); !known || sees {
		t.Fatal("rejection not remembered")
	}
	if c.describeRejectedImages(context.Background(), rejected, "BEDROCK", profileARN, &turn) {
		t.Fatal("nothing left to rewrite: no second resend")
	}
}

// The agent loop resends a turn whose images the provider refused, with the
// images described, on the route that served the turn.
func TestAgentResendAfterImageRejection(t *testing.T) {
	c, m := visionTestCLI(t, "BEDROCK")
	c.unattended = true
	m.listing["BEDROCK"] = invokableVisionListing(t)
	img := models.ImageContent{MediaType: "image/png", Data: probePNG}
	c.history = []models.Message{{Role: "user", Content: "screenshot", Images: []models.ImageContent{img}}}
	turn := append([]models.Message(nil), c.history...)
	a := &AgentMode{cli: c, logger: zap.NewNop()}
	rejected := errors.New("ValidationException: This model doesn't support the image content block")
	if !a.resendTurnAfter(context.Background(), rejected, &turn) {
		t.Fatal("image rejection not resent")
	}
	if len(turn[0].Images) != 0 || !strings.Contains(turn[0].Content, "described: a login form") {
		t.Fatalf("outgoing turn not rewritten: %+v", turn[0])
	}
	if sees, known := learnedVision.lookup(c.stateRoot, "BEDROCK", profileARN); !known || sees {
		t.Fatal("the agent's route was not remembered as blind")
	}
	if a.resendTurnAfter(context.Background(), rejected, &turn) {
		t.Fatal("resent twice")
	}
}
