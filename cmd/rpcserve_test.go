package cmd

import (
	"context"
	"testing"
	"time"

	"strings"

	"github.com/diillson/chatcli/cli/rpcserve"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/manager"
	"github.com/diillson/chatcli/models"
)

// fakeClient is a minimal client.LLMClient.
type fakeClient struct {
	reply    string
	lastHist int
}

func (f *fakeClient) GetModelName() string { return "fake" }
func (f *fakeClient) SendPrompt(_ context.Context, _ string, history []models.Message, _ int) (string, error) {
	f.lastHist = len(history)
	return f.reply, nil
}

// fakeManager embeds the interface (so unimplemented methods exist) and only
// overrides GetClient, which is all rpcBackend.Prompt uses.
type fakeManager struct {
	manager.LLMManager
	client      *fakeClient
	noProviders bool
}

func (m *fakeManager) GetClient(string, string) (client.LLMClient, error) { return m.client, nil }

// providers defaults to one fake provider so HasLLM is true; tests pin the
// no-provider surface by setting noProviders.
func (m *fakeManager) GetAvailableProviders() []string {
	if m.noProviders {
		return nil
	}
	return []string{"FAKE"}
}

func TestFirstNonEmpty(t *testing.T) {
	if firstNonEmpty("", "", "x", "y") != "x" {
		t.Error("should return first non-empty")
	}
	if firstNonEmpty("", "") != "" {
		t.Error("all empty -> empty")
	}
}

func TestRPCBackend_NoCLI(t *testing.T) {
	b := &rpcBackend{mgr: &fakeManager{client: &fakeClient{}}, sessions: map[string][]models.Message{}} // cli is nil
	if _, err := b.Agent(context.Background(), "s", "t", rpcserve.RunOpts{}); err == nil {
		t.Error("Agent should error when ChatCLI is unavailable")
	}
	if _, err := b.Coder(context.Background(), "s", "t", rpcserve.RunOpts{}); err == nil {
		t.Error("Coder should error when ChatCLI is unavailable")
	}
	if _, err := b.AgentStream(context.Background(), "s", "t", rpcserve.RunOpts{}); err == nil {
		t.Error("AgentStream should error when ChatCLI is unavailable")
	}
	if _, err := b.CallTool(context.Background(), "read", "x"); err == nil {
		t.Error("CallTool should error when ChatCLI is unavailable")
	}
	if b.Tools() != nil {
		t.Error("Tools should be nil when ChatCLI is unavailable")
	}
	if b.Skills() != nil {
		t.Error("Skills should be nil when ChatCLI is unavailable")
	}
	if _, err := b.SkillContent("x"); err == nil {
		t.Error("SkillContent should error when ChatCLI is unavailable")
	}
	if _, err := b.ProvidersJSON(); err == nil {
		t.Error("ProvidersJSON should error when ChatCLI is unavailable")
	}
}

// TestRPCBackend_PromptWithRouting pins the per-call provider/model routing
// on the chat path: options select a distinct client via the manager while
// the shared session history is preserved.
// TestRPCBackend_NoProvider pins the actionable error every LLM-backed
// call returns when the instance has no provider configured — direct tools
// are unaffected and HasLLM drives the MCP tool listing.
func TestRPCBackend_NoProvider(t *testing.T) {
	b := &rpcBackend{mgr: &fakeManager{noProviders: true}, sessions: map[string][]models.Message{}}
	if b.HasLLM() {
		t.Fatal("HasLLM must be false without providers")
	}
	if _, err := b.Prompt(context.Background(), "s", "q"); err == nil || !strings.Contains(err.Error(), "no LLM provider") {
		t.Fatalf("Prompt must return the actionable no-provider error, got %v", err)
	}
	if _, err := b.Agent(context.Background(), "s", "t", rpcserve.RunOpts{}); err == nil || !strings.Contains(err.Error(), "no LLM provider") {
		t.Fatalf("Agent must return the actionable no-provider error, got %v", err)
	}
}

func TestRPCBackend_PromptWithRouting(t *testing.T) {
	fc := &fakeClient{reply: "routed"}
	b := &rpcBackend{
		mgr:      &fakeManager{client: fc},
		provider: "OPENAI",
		sessions: map[string][]models.Message{},
	}
	out, err := b.PromptWith(context.Background(), "s1", "q", rpcserve.RunOpts{Provider: "DEVIN", Model: "gpt-5.6-sol"})
	if err != nil || out != "routed" {
		t.Fatalf("PromptWith: %q %v", out, err)
	}
	if len(b.sessions["s1"]) == 0 {
		t.Error("routed chat must still persist session history")
	}
	// No routing options: falls back to the plain Prompt path.
	if out, err := b.PromptWith(context.Background(), "s1", "q2", rpcserve.RunOpts{}); err != nil || out != "routed" {
		t.Fatalf("PromptWith default: %q %v", out, err)
	}
}

func TestRPCBackendPrompt(t *testing.T) {
	fc := &fakeClient{reply: "answer"}
	b := &rpcBackend{
		mgr:      &fakeManager{client: fc},
		sessions: map[string][]models.Message{},
	}

	out, err := b.Prompt(context.Background(), "s1", "hello")
	if err != nil || out != "answer" {
		t.Fatalf("prompt: %q %v", out, err)
	}
	// History is retained per session: a second turn sees the prior turns.
	if _, err := b.Prompt(context.Background(), "s1", "again"); err != nil {
		t.Fatal(err)
	}
	if fc.lastHist < 3 {
		t.Errorf("expected accumulated history (>=3), got %d", fc.lastHist)
	}
	// A different session starts fresh.
	if _, err := b.Prompt(context.Background(), "s2", "hi"); err != nil {
		t.Fatal(err)
	}
	if fc.lastHist != 1 {
		t.Errorf("new session should start with 1 message, got %d", fc.lastHist)
	}
}

func TestRPCBackendPrompt_HistoryCap(t *testing.T) {
	fc := &fakeClient{reply: "ok"}
	b := &rpcBackend{mgr: &fakeManager{client: fc}, sessions: map[string][]models.Message{}}
	for i := 0; i < rpcMaxHistory; i++ {
		if _, err := b.Prompt(context.Background(), "s", "msg"); err != nil {
			t.Fatal(err)
		}
	}
	b.mu.Lock()
	n := len(b.sessions["s"])
	b.mu.Unlock()
	if n > rpcMaxHistory {
		t.Errorf("history should be capped at %d, got %d", rpcMaxHistory, n)
	}
}

// fakeStore is an in-memory sessionStore for ManageSession tests. mtimes is
// settable so binding-refresh tests can simulate another surface's write.
type fakeStore struct {
	saved  map[string][]models.Message
	mtimes map[string]time.Time
}

func newFakeStore() *fakeStore {
	return &fakeStore{saved: map[string][]models.Message{}, mtimes: map[string]time.Time{}}
}

func (s *fakeStore) SaveSessionRPC(name string, history []models.Message) error {
	s.saved[name] = append([]models.Message(nil), history...)
	if s.mtimes == nil {
		s.mtimes = map[string]time.Time{}
	}
	s.mtimes[name] = time.Now()
	return nil
}

func (s *fakeStore) LoadSessionRPC(name string) ([]models.Message, error) {
	h, ok := s.saved[name]
	if !ok {
		return nil, errCLI("sessão não encontrada: " + name)
	}
	return h, nil
}

func (s *fakeStore) ListSessionsRPC() ([]string, error) {
	names := make([]string, 0, len(s.saved))
	for n := range s.saved {
		names = append(names, n)
	}
	return names, nil
}

func (s *fakeStore) DeleteSessionRPC(name string) error {
	if _, ok := s.saved[name]; !ok {
		return errCLI("sessão não encontrada: " + name)
	}
	delete(s.saved, name)
	return nil
}

func (s *fakeStore) PruneSessionsRPC(string, int) int { return 0 }

func (s *fakeStore) SessionModTimeRPC(name string) (time.Time, error) {
	if _, ok := s.saved[name]; !ok {
		return time.Time{}, errCLI("sessão não encontrada: " + name)
	}
	return s.mtimes[name], nil
}

func (s *fakeStore) SessionExistsRPC(name string) bool {
	_, ok := s.saved[name]
	return ok
}

func sessionBackend(store sessionStore) *rpcBackend {
	return &rpcBackend{store: store, sessions: map[string][]models.Message{}}
}

func sessionMsgs(n int) []models.Message {
	out := make([]models.Message, n)
	for i := range out {
		out[i] = models.Message{Role: "user", Content: "m"}
	}
	return out
}

// TestManageSession_SaveLoadRoundTrip pins the essential flow: an MCP client
// saves the live conversation behind a session id and later restores it —
// including into a different live session — with the live-history cap applied.
func TestManageSession_SaveLoadRoundTrip(t *testing.T) {
	store := newFakeStore()
	b := sessionBackend(store)
	ctx := context.Background()
	b.sessions["mcp"] = sessionMsgs(4)

	out, err := b.ManageSession(ctx, "save", "mcp", "projeto-x")
	if err != nil || !strings.Contains(out, `"projeto-x"`) || !strings.Contains(out, "4 messages") {
		t.Fatalf("save failed: %q, %v", out, err)
	}
	if len(store.saved["projeto-x"]) != 4 {
		t.Fatalf("store must hold the saved history, got %d", len(store.saved["projeto-x"]))
	}

	out, err = b.ManageSession(ctx, "load", "outra", "projeto-x")
	if err != nil || !strings.Contains(out, "4 messages") {
		t.Fatalf("load failed: %q, %v", out, err)
	}
	if len(b.sessions["outra"]) != 4 {
		t.Fatalf("load must hydrate the live session, got %d", len(b.sessions["outra"]))
	}

	store.saved["longa"] = sessionMsgs(rpcMaxHistory + 10)
	if _, err = b.ManageSession(ctx, "load", "mcp", "longa"); err != nil {
		t.Fatal(err)
	}
	if len(b.sessions["mcp"]) != rpcMaxHistory {
		t.Errorf("load must trim to rpcMaxHistory, got %d", len(b.sessions["mcp"]))
	}
}

// TestManageSession_SaveDefaultsNameToSession pins the name fallback.
func TestManageSession_SaveDefaultsNameToSession(t *testing.T) {
	store := newFakeStore()
	b := sessionBackend(store)
	b.sessions["minha-sessao"] = sessionMsgs(1)
	if _, err := b.ManageSession(context.Background(), "save", "minha-sessao", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.saved["minha-sessao"]; !ok {
		t.Error("save without a name must persist under the session id")
	}
}

func TestManageSession_ActiveAndClear(t *testing.T) {
	b := sessionBackend(nil) // active/clear need no store
	ctx := context.Background()

	out, err := b.ManageSession(ctx, "active", "", "")
	if err != nil || !strings.Contains(out, "no live sessions") {
		t.Fatalf("empty active wrong: %q, %v", out, err)
	}

	b.sessions["a"] = sessionMsgs(2)
	b.sessions["b"] = sessionMsgs(5)
	out, err = b.ManageSession(ctx, "active", "", "")
	if err != nil || !strings.Contains(out, "a (2 messages)") || !strings.Contains(out, "b (5 messages)") {
		t.Fatalf("active listing wrong: %q, %v", out, err)
	}

	if out, err = b.ManageSession(ctx, "clear", "a", ""); err != nil || !strings.Contains(out, "cleared") {
		t.Fatalf("clear wrong: %q, %v", out, err)
	}
	if _, still := b.sessions["a"]; still {
		t.Error("clear must drop the live session")
	}
	if out, _ = b.ManageSession(ctx, "clear", "ghost", ""); !strings.Contains(out, "no live history") {
		t.Errorf("clearing an unknown session must say so: %q", out)
	}
}

func TestManageSession_ListAndDelete(t *testing.T) {
	store := newFakeStore()
	store.saved["s1"] = sessionMsgs(1)
	b := sessionBackend(store)
	ctx := context.Background()

	out, err := b.ManageSession(ctx, "list", "", "")
	if err != nil || !strings.Contains(out, "s1") {
		t.Fatalf("list wrong: %q, %v", out, err)
	}

	if out, err = b.ManageSession(ctx, "delete", "", "s1"); err != nil || !strings.Contains(out, "deleted") {
		t.Fatalf("delete wrong: %q, %v", out, err)
	}
	if out, err = b.ManageSession(ctx, "list", "", ""); err != nil || !strings.Contains(out, "empty") {
		t.Fatalf("post-delete list wrong: %q, %v", out, err)
	}
}

// TestManageSession_Guards pins every error path an MCP client can hit.
func TestManageSession_Guards(t *testing.T) {
	ctx := context.Background()

	b := sessionBackend(newFakeStore())
	if _, err := b.ManageSession(ctx, "save", "vazia", ""); err == nil || !strings.Contains(err.Error(), "no messages") {
		t.Errorf("saving an empty session must fail actionably, got %v", err)
	}
	if _, err := b.ManageSession(ctx, "load", "mcp", ""); err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Errorf("load without name must fail, got %v", err)
	}
	if _, err := b.ManageSession(ctx, "delete", "", ""); err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Errorf("delete without name must fail, got %v", err)
	}
	if _, err := b.ManageSession(ctx, "explode", "", ""); err == nil || !strings.Contains(err.Error(), "unknown action") {
		t.Errorf("unknown action must fail, got %v", err)
	}

	// Without a store (ChatCLI init failed) every store-backed action fails
	// with the availability error; the live-session ones keep working.
	nb := sessionBackend(nil)
	nb.sessions["x"] = sessionMsgs(1)
	if _, err := nb.ManageSession(ctx, "save", "x", ""); err == nil {
		t.Error("save without a store must fail")
	}
	if _, err := nb.ManageSession(ctx, "list", "", ""); err == nil {
		t.Error("list without a store must fail")
	}
	if _, err := nb.ManageSession(ctx, "load", "x", "algum"); err == nil {
		t.Error("load without a store must fail")
	}
	if _, err := nb.ManageSession(ctx, "delete", "", "algum"); err == nil {
		t.Error("delete without a store must fail")
	}
}

// A rebuilt manager must reach the backend: the ACP/MCP server holds its own
// reference, and a `/config reload` from the client that only swapped
// ChatCLI's would leave every later prompt on the pre-reload provider set.
func TestRPCBackend_SetManagerSwapsTheLiveManager(t *testing.T) {
	first := &manager.LLMManagerImpl{}
	second := &manager.LLMManagerImpl{}
	b := &rpcBackend{mgr: first}

	if b.manager() != manager.LLMManager(first) {
		t.Fatal("backend must start on the manager it was built with")
	}
	b.setManager(second)
	if b.manager() != manager.LLMManager(second) {
		t.Fatal("setManager must swap the live manager")
	}
	b.setManager(nil)
	if b.manager() != manager.LLMManager(second) {
		t.Fatal("a nil rebuild must never blank the manager")
	}
}
