package cli

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/workspace"
	"github.com/diillson/chatcli/cli/workspace/memory"
	"github.com/diillson/chatcli/llm/embedding"
	"go.uber.org/zap"
)

// resetHydeProvider clears the session-cached embedding provider and
// restores whatever was latched once the test finishes, so tests can
// exercise the lazy-build/refresh cycle without leaking state.
func resetHydeProvider(t *testing.T) {
	t.Helper()
	hydeMu.Lock()
	prevProvider, prevReady := hydeProvider, hydeProviderReady
	hydeProvider, hydeProviderReady = nil, false
	hydeMu.Unlock()
	t.Cleanup(func() {
		hydeMu.Lock()
		hydeProvider, hydeProviderReady = prevProvider, prevReady
		hydeMu.Unlock()
	})
}

func TestHydeProviderForSession_LatchesUntilRefresh(t *testing.T) {
	resetHydeProvider(t)
	cli := &ChatCLI{logger: zap.NewNop()}

	// No provider configured at first use → null latched for the session.
	t.Setenv("CHATCLI_EMBED_PROVIDER", "")
	if p := cli.hydeProviderForSession(); !embedding.IsNull(p) {
		t.Fatalf("expected null provider with empty env, got %s", p.Name())
	}

	// Setting the env afterwards must NOT change the latched provider —
	// this is exactly the .env-edited-mid-session scenario.
	t.Setenv("CHATCLI_EMBED_PROVIDER", "bedrock")
	if p := cli.hydeProviderForSession(); !embedding.IsNull(p) {
		t.Fatal("provider must stay latched until an explicit refresh")
	}

	// /reload calls refreshEmbeddingProvider → the new env takes effect.
	oldName, newName := cli.refreshEmbeddingProvider()
	if oldName != "null" {
		t.Errorf("oldName = %q, want null", oldName)
	}
	if !strings.HasPrefix(newName, "bedrock:") {
		t.Errorf("newName = %q, want bedrock:*", newName)
	}
	if p := cli.hydeProviderForSession(); embedding.IsNull(p) {
		t.Fatal("expected a real provider after refresh")
	} else if !strings.HasPrefix(p.Name(), "bedrock:") {
		t.Errorf("provider = %q, want bedrock:*", p.Name())
	}
}

func TestRefreshEmbeddingProvider_UnknownNameFallsBackToNull(t *testing.T) {
	resetHydeProvider(t)
	cli := &ChatCLI{logger: zap.NewNop()}

	t.Setenv("CHATCLI_EMBED_PROVIDER", "does-not-exist")
	if _, newName := cli.refreshEmbeddingProvider(); newName != "null" {
		t.Errorf("unknown provider must fall back to null, got %q", newName)
	}
	if p := cli.hydeProviderForSession(); !embedding.IsNull(p) {
		t.Fatal("expected null provider after failed construction")
	}
}

func TestRefreshEmbeddingProvider_SwapsMemoryVectorIndex(t *testing.T) {
	resetHydeProvider(t)
	ms := workspace.NewMemoryStore(t.TempDir(), zap.NewNop())
	cli := &ChatCLI{logger: zap.NewNop(), memoryStore: ms}

	// Simulate a session that already attached an index for provider A.
	t.Setenv("CHATCLI_EMBED_PROVIDER", "voyage")
	t.Setenv("VOYAGE_API_KEY", "test-key")
	first := cli.hydeProviderForSession()
	ms.AttachVectorIndex(memory.NewVectorIndex(ms.Manager().MemoryDir(), first, zap.NewNop()))

	// Provider changes in .env → refresh must swap the attached index.
	t.Setenv("CHATCLI_EMBED_PROVIDER", "bedrock")
	if _, newName := cli.refreshEmbeddingProvider(); !strings.HasPrefix(newName, "bedrock:") {
		t.Fatalf("newName = %q, want bedrock:*", newName)
	}
	idx := ms.VectorIndex()
	if idx == nil {
		t.Fatal("expected vector index to stay attached after provider swap")
	}
	if got := idx.ProviderName(); !strings.HasPrefix(got, "bedrock:") {
		t.Errorf("vector index provider = %q, want bedrock:*", got)
	}

	// Embeddings turned off → refresh must detach the index.
	t.Setenv("CHATCLI_EMBED_PROVIDER", "")
	if _, newName := cli.refreshEmbeddingProvider(); newName != "null" {
		t.Fatalf("newName = %q, want null", newName)
	}
	if ms.VectorIndex() != nil {
		t.Error("expected vector index detached when provider is removed")
	}
}

func TestRefreshEmbeddingProvider_RewiresContextRetrieval(t *testing.T) {
	resetHydeProvider(t)
	ch, err := NewContextHandler(zap.NewNop())
	if err != nil {
		t.Skipf("NewContextHandler unavailable in this environment: %v", err)
	}
	cli := &ChatCLI{logger: zap.NewNop(), contextHandler: ch}

	t.Setenv("CHATCLI_EMBED_PROVIDER", "bedrock")
	cli.refreshEmbeddingProvider()
	if !ch.GetManager().RetrievalEnabled() {
		t.Fatal("expected /context retrieval enabled after refresh with a real provider")
	}

	t.Setenv("CHATCLI_EMBED_PROVIDER", "")
	cli.refreshEmbeddingProvider()
	if ch.GetManager().RetrievalEnabled() {
		t.Fatal("expected /context retrieval disabled after provider removal")
	}
}

func TestEmbeddingProviderLabel(t *testing.T) {
	if got := embeddingProviderLabel(nil); got != "null" {
		t.Errorf("label(nil) = %q, want null", got)
	}
	if got := embeddingProviderLabel(embedding.NewNull()); got != "null" {
		t.Errorf("label(Null) = %q, want null", got)
	}
	t.Setenv("CHATCLI_EMBED_PROVIDER", "bedrock")
	p, err := embedding.NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	if got := embeddingProviderLabel(p); !strings.HasPrefix(got, "bedrock:") {
		t.Errorf("label = %q, want bedrock:*", got)
	}
}
