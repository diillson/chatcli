/*
 * ChatCLI - Command Line Interface for LLM interaction
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

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/evals"
	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/pkg/pulse/pulsetest"
	"go.uber.org/zap"
)

// parentClient answers a fixed reply (or error) and records the innermost
// dashboard parent of every request, so a test can prove the model call
// hangs from the node that made it.
type parentClient struct {
	reply   string
	err     error
	tokens  int
	mu      sync.Mutex
	parents []string
}

func (c *parentClient) GetModelName() string { return "parent-fake" }
func (c *parentClient) SendPrompt(ctx context.Context, _ string, _ []models.Message, _ int) (string, error) {
	c.record(ctx)
	return c.reply, c.err
}
func (c *parentClient) CountTokens(ctx context.Context, _ string, _ []models.Message) (int, error) {
	c.record(ctx)
	return c.tokens, c.err
}
func (c *parentClient) record(ctx context.Context) {
	c.mu.Lock()
	c.parents = append(c.parents, pulse.ParentFrom(ctx))
	c.mu.Unlock()
}
func (c *parentClient) lastParent() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.parents) == 0 {
		return ""
	}
	return c.parents[len(c.parents)-1]
}

// parentClientManager hands out one parentClient for every route.
type parentClientManager struct {
	*minimalManager
	c client.LLMClient
}

func (m *parentClientManager) GetClient(string, string) (client.LLMClient, error) { return m.c, nil }

func newMediaPulseCLI(fake *parentClient) *ChatCLI {
	return &ChatCLI{
		Provider: "FAKE", Model: "fake-model", logger: zap.NewNop(),
		manager: &parentClientManager{minimalManager: &minimalManager{providers: []string{"FAKE"}}, c: fake},
	}
}

// assertNoLeak fails when any attribute carries one of the secrets.
func assertNoLeak(t *testing.T, rec *pulsetest.Recorder, secrets ...string) {
	t.Helper()
	for _, e := range rec.Events() {
		for k, v := range e.Attrs {
			for _, s := range secrets {
				if strings.Contains(v, s) {
					t.Fatalf("%s leaked onto the bus in %s.%s: %q", s, e.Name, k, v)
				}
			}
		}
	}
}

func TestVisionDescribeIsAPatternWithTheModelCallUnderIt(t *testing.T) {
	t.Setenv("CHATCLI_VISION_PROVIDER", "FAKE")
	t.Setenv("CHATCLI_VISION_MODEL", "fake-vision")
	rec := pulsetest.Record(t)
	fake := &parentClient{reply: "SECRET-DESCRIPTION"}
	c := newMediaPulseCLI(fake)
	ctx := pulse.WithParent(context.Background(), "turn-1")

	img := models.ImageContent{MediaType: "image/png", Data: []byte("0123456789"), FileName: "SECRET-NAME.png"}
	if _, ok := c.describeImagesFallback(ctx, []models.ImageContent{img}); !ok {
		t.Fatal("describe failed")
	}
	start := rec.WaitFor(t, "describe start", pulsetest.Is(pulse.KindPattern, pulseVisionDescribe, pulse.PhaseStart))
	ev := rec.WaitFor(t, "describe end", pulsetest.Is(pulse.KindPattern, pulseVisionDescribe, pulse.PhaseEnd))
	if ev.Parent != "turn-1" || ev.Status != pulse.StatusOK || ev.Attrs["state"] != "described" ||
		ev.Attrs["images"] != "1" || ev.Attrs["bytes_in"] != "10" || ev.Attrs["provider"] != "fake" || ev.Attrs["model"] != "fake-vision" {
		t.Errorf("describe = %+v", ev)
	}
	if fake.lastParent() != start.ID {
		t.Errorf("the vision call hangs from %q, want the pattern %q", fake.lastParent(), start.ID)
	}
	assertNoLeak(t, rec, "SECRET")

	// No vision model anywhere: the node says so.
	t.Setenv("CHATCLI_VISION_PROVIDER", "")
	c.manager = &parentClientManager{minimalManager: &minimalManager{}, c: fake}
	if _, ok := c.describeImagesFallback(ctx, []models.ImageContent{img}); ok {
		t.Fatal("expected no vision model")
	}
	rec.WaitFor(t, "no-model end", func(e pulse.Event) bool {
		return e.Name == pulseVisionDescribe && e.Phase == pulse.PhaseEnd && e.Attrs["state"] == "no vision model" && e.Status == pulse.StatusError
	})
}

func TestPDFRouteTranscribeAndExtractShowOnDashboard(t *testing.T) {
	t.Setenv("CHATCLI_PDF_INPUT", "")
	t.Setenv("CHATCLI_VISION_PROVIDER", "CLAUDEAI")
	t.Setenv("CHATCLI_VISION_MODEL", "claude-pdf-test")
	rec := pulsetest.Record(t)
	fake := &parentClient{reply: "--- page 1 ---\nSECRET-TRANSCRIPT"}
	c := newMediaPulseCLI(fake)
	ctx := pulse.WithParent(context.Background(), "turn-2")

	scanned := models.DocumentContent{MediaType: models.PDFMediaType, Data: []byte("%PDF-1.7 SECRET"), FileName: "SECRET-scan.pdf", Pages: 3}
	got := c.gateDocumentsForRoute(ctx, "XAI", "grok-4.20", []models.DocumentContent{scanned})
	if len(got) != 1 || !strings.Contains(got[0].Text, "SECRET-TRANSCRIPT") {
		t.Fatalf("got %+v", got)
	}
	route := rec.WaitFor(t, "route point", pulsetest.Is(pulse.KindPattern, pulsePDFRoute, pulse.PhasePoint))
	if route.Parent != "turn-2" || route.Attrs["route"] != "text" || route.Attrs["documents"] != "1" || route.Attrs["pages"] != "3" {
		t.Errorf("route = %+v", route)
	}
	start := rec.WaitFor(t, "transcribe start", pulsetest.Is(pulse.KindPattern, pulsePDFTranscribe, pulse.PhaseStart))
	ev := rec.WaitFor(t, "transcribe end", pulsetest.Is(pulse.KindPattern, pulsePDFTranscribe, pulse.PhaseEnd))
	if ev.Parent != "turn-2" || ev.Attrs["state"] != "transcribed" || ev.Attrs["attempts"] != "1" ||
		ev.Attrs["provider"] != "claudeai" || ev.Attrs["model"] != "claude-pdf-test" || ev.Attrs["pages"] != "3" {
		t.Errorf("transcribe = %+v", ev)
	}
	if fake.lastParent() != start.ID {
		t.Errorf("the transcription call hangs from %q, want %q", fake.lastParent(), start.ID)
	}

	// A native route sends the document as is.
	c.gateDocumentsForRoute(ctx, "CLAUDEAI", "claude-x", []models.DocumentContent{scanned})
	rec.WaitFor(t, "native route", func(e pulse.Event) bool {
		return e.Name == pulsePDFRoute && e.Attrs["route"] == "native"
	})

	// Local extraction of a real PDF.
	data, err := os.ReadFile(pdfFixture)
	if err != nil {
		t.Fatal(err)
	}
	doc := c.readPDF(ctx, data, "SECRET-report.pdf")
	ext := rec.WaitFor(t, "extract end", pulsetest.Is(pulse.KindTool, pulsePDFExtract, pulse.PhaseEnd))
	if ext.Parent != "turn-2" || ext.Attrs["pages"] != "2" || ext.Attrs["chars_out"] == "" || ext.Attrs["chars_out"] == "0" ||
		ext.Attrs["scanned"] != "" || ext.Status != pulse.StatusOK || doc.Pages != 2 {
		t.Errorf("extract = %+v", ext)
	}
	assertNoLeak(t, rec, "SECRET")
}

func TestPDFTranscribeFailureCountsAttempts(t *testing.T) {
	t.Setenv("CHATCLI_VISION_PROVIDER", "CLAUDEAI")
	t.Setenv("CHATCLI_VISION_MODEL", "claude-pdf-test")
	rec := pulsetest.Record(t)
	c := newMediaPulseCLI(&parentClient{err: errors.New("SECRET-ERROR")})
	if _, _, err := c.transcribePDF(context.Background(), models.DocumentContent{Data: []byte("%PDF")}); err == nil {
		t.Fatal("expected failure")
	}
	ev := rec.WaitFor(t, "failed transcribe", pulsetest.Is(pulse.KindPattern, pulsePDFTranscribe, pulse.PhaseEnd))
	if ev.Status != pulse.StatusError || ev.Attrs["state"] != "transcription failed" || ev.Attrs["attempts"] == "" {
		t.Errorf("transcribe = %+v", ev)
	}
	assertNoLeak(t, rec, "SECRET")
}

func TestDocsImageDescriberIsANodeWithItsCallUnderIt(t *testing.T) {
	t.Setenv("CHATCLI_VISION_PROVIDER", "FAKE")
	t.Setenv("CHATCLI_VISION_MODEL", "fake-vision")
	t.Setenv("CHATCLI_VISION_COMPRESS", "off")
	rec := pulsetest.Record(t)
	fake := &parentClient{reply: "SECRET-DESCRIPTION"}
	d := &docsImageDescriber{cli: newMediaPulseCLI(fake)}
	ctx := pulse.WithParent(context.Background(), "tool-8")
	if _, by, err := d.DescribeImage(ctx, "SECRET-PROMPT", models.ImageContent{MediaType: "image/png", Data: []byte("abcd")}); err != nil || by != "fake/fake-vision" {
		t.Fatalf("by=%q err=%v", by, err)
	}
	start := rec.WaitFor(t, "describe start", pulsetest.Is(pulse.KindTool, pulseDocsDescribe, pulse.PhaseStart))
	ev := rec.WaitFor(t, "describe end", pulsetest.Is(pulse.KindTool, pulseDocsDescribe, pulse.PhaseEnd))
	if ev.Parent != "tool-8" || ev.Attrs["bytes_in"] != "4" || ev.Attrs["provider"] != "fake" || ev.Attrs["model"] != "fake-vision" || ev.Status != pulse.StatusOK {
		t.Errorf("describe = %+v", ev)
	}
	if fake.lastParent() != start.ID {
		t.Errorf("the vision call hangs from %q, want %q", fake.lastParent(), start.ID)
	}
	assertNoLeak(t, rec, "SECRET")
}

func TestExactTokenCountIsANode(t *testing.T) {
	rec := pulsetest.Record(t)
	c := newTenantTestCLI(t)
	c.Provider, c.Model = "TESTPROV", "count-model"
	c.history = []models.Message{{Role: "user", Content: "SECRET-HISTORY " + strings.Repeat("a", 400)}}
	fake := &parentClient{tokens: 120}
	c.Client = fake
	if n, ok := c.calibrateExact(pulse.WithParent(context.Background(), "turn-3")); !ok || n != 120 {
		t.Fatalf("n=%d ok=%v", n, ok)
	}
	start := rec.WaitFor(t, "count start", pulsetest.Is(pulse.KindTool, pulseTokensCount, pulse.PhaseStart))
	ev := rec.WaitFor(t, "count end", pulsetest.Is(pulse.KindTool, pulseTokensCount, pulse.PhaseEnd))
	if ev.Parent != "turn-3" || ev.Attrs["tokens"] != "120" || ev.Attrs["provider"] != "testprov" ||
		ev.Attrs["model"] != "count-model" || ev.Attrs["messages"] != "1" || ev.Status != pulse.StatusOK {
		t.Errorf("count = %+v", ev)
	}
	if fake.lastParent() != start.ID {
		t.Errorf("the count call hangs from %q, want %q", fake.lastParent(), start.ID)
	}
	assertNoLeak(t, rec, "SECRET")
}

func TestEvalJudgeAndRecordShowOnDashboard(t *testing.T) {
	rec := pulsetest.Record(t)
	fake := &parentClient{reply: `{"score":0.9}`}
	c := newMediaPulseCLI(fake)
	j := &EvalJudge{cli: c, client: fake, provider: "FAKE", model: "judge-model"}
	if _, _, err := j.Send(pulse.WithParent(context.Background(), "background-4"), "SECRET-GRADING-PROMPT"); err != nil {
		t.Fatal(err)
	}
	start := rec.WaitFor(t, "judge start", pulsetest.Is(pulse.KindTool, pulseEvalJudge, pulse.PhaseStart))
	ev := rec.WaitFor(t, "judge end", pulsetest.Is(pulse.KindTool, pulseEvalJudge, pulse.PhaseEnd))
	if ev.Parent != "background-4" || ev.Attrs["provider"] != "fake" || ev.Attrs["model"] != "judge-model" || ev.Status != pulse.StatusOK {
		t.Errorf("judge = %+v", ev)
	}
	if fake.lastParent() != start.ID {
		t.Errorf("the judge call hangs from %q, want %q", fake.lastParent(), start.ID)
	}

	t.Setenv(evals.RecordEnv, filepath.Join(t.TempDir(), "record.json"))
	c.history = []models.Message{{Role: "user", Content: "SECRET-Q"}, {Role: "assistant", Content: "SECRET-A"}}
	c.writeEvalRecord(evalModeChat, errors.New("SECRET-RUN-ERROR"))
	point := rec.WaitFor(t, "record point", pulsetest.Is(pulse.KindBackground, pulseEvalRecordName, pulse.PhasePoint))
	if point.Status != pulse.StatusError || point.Attrs["mode"] != evalModeChat || point.Attrs["turns"] != "1" ||
		point.Attrs["provider"] != "fake" || point.Attrs["model"] != "fake-model" {
		t.Errorf("record = %+v", point)
	}
	assertNoLeak(t, rec, "SECRET")
}
