package cli

import (
	"context"
	"testing"

	llmclient "github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// warmClient is a mock client that can refresh its cache entry.
type warmClient struct {
	llmclient.MockLLMClient
	calls int
	err   error
}

func (w *warmClient) KeepPromptCacheWarm(context.Context) (*models.UsageInfo, error) {
	w.calls++
	if w.err != nil {
		return nil, w.err
	}
	return &models.UsageInfo{IsReal: true, CacheReadInputTokens: 40000}, nil
}

func keepAliveCLI(t *testing.T) *ChatCLI {
	t.Helper()
	t.Setenv(PromptCacheKeepAliveEnv, "")
	t.Setenv(llmclient.PromptCacheTTLEnv, "")
	llmclient.ResetPromptCacheTTL()
	llmclient.SetPromptCacheKeepAlivePreferred(false)
	t.Cleanup(func() {
		llmclient.ResetPromptCacheTTL()
		llmclient.SetPromptCacheKeepAlivePreferred(false)
	})
	cli := &ChatCLI{logger: zap.NewNop(), costTracker: NewCostTrackerAt(t.TempDir())}
	cli.costTracker.SetRealUsageHook(cli.noteRealUsageForKeepAlive)
	t.Cleanup(cli.cancelPromptCacheKeepAlive)
	return cli
}

// Auto arms only where a read is cheap enough to beat the hour-long
// entry: Fable 5.1 yes, Sonnet no; the env forces either way; the
// gateway never arms.
func TestKeepAliveAppliesByReadPrice(t *testing.T) {
	cli := keepAliveCLI(t)
	if !cli.keepAliveApplies("CLAUDEAI", "claude-fable-5-1") {
		t.Error("Fable 5.1 reads at 2.5% of input: keep-alive beats the hour")
	}
	if cli.keepAliveApplies("CLAUDEAI", "claude-sonnet-5") {
		t.Error("Sonnet reads at 10% of input: the hour-long entry wins")
	}
	if cli.keepAliveApplies("OPENAI", "gpt-5.6") {
		t.Error("no keep-alive request exists outside the Anthropic wire")
	}
	t.Setenv(PromptCacheKeepAliveEnv, "off")
	if cli.keepAliveApplies("CLAUDEAI", "claude-fable-5-1") {
		t.Error("off must win")
	}
	t.Setenv(PromptCacheKeepAliveEnv, "on")
	if !cli.keepAliveApplies("CLAUDEAI", "claude-sonnet-5") {
		t.Error("on arms every client that can refresh")
	}
	cli.unattended = true
	if cli.keepAliveApplies("CLAUDEAI", "claude-fable-5-1") {
		t.Error("the gateway serves many conversations through one client; never arm there")
	}
}

// Every booked request restarts the schedule for a qualifying route and
// pins the short lifetime; a route that stops qualifying cancels it.
func TestKeepAliveArmsFromBookedUsageAndPinsShortTTL(t *testing.T) {
	cli := keepAliveCLI(t)
	llmclient.SetPromptCacheTTLHint("1h") // what a coder run asks for
	cli.costTracker.RecordRealUsage("CLAUDEAI", "claude-fable-5-1", realUsage(100, 0, 30000))
	ka := &cli.cacheKeepAlive
	ka.mu.Lock()
	armed := ka.timer != nil && ka.model == "claude-fable-5-1"
	ka.mu.Unlock()
	if !armed {
		t.Fatal("a Fable request must arm the keep-alive")
	}
	if !llmclient.PromptCacheKeepAlivePreferred() || llmclient.AnthropicCacheTTL() != "5m" {
		t.Fatalf("keep-alive keeps the 5m entry even when the surface asked for 1h, got ttl=%s", llmclient.AnthropicCacheTTL())
	}
	cli.costTracker.RecordRealUsage("CLAUDEAI", "claude-sonnet-5", realUsage(100, 0, 30000))
	ka.mu.Lock()
	stillArmed := ka.timer != nil
	ka.mu.Unlock()
	if stillArmed || llmclient.PromptCacheKeepAlivePreferred() {
		t.Fatal("a route that does not qualify cancels the schedule")
	}
}

// A refresh sends one no-output request, books its usage without
// counting as a turn for the schedule, re-arms, and stops after the idle
// stretch or on the first failure. A stale generation is a no-op.
func TestKeepAliveFireRefreshesBooksAndRearms(t *testing.T) {
	cli := keepAliveCLI(t)
	wc := &warmClient{}
	cli.Client = wc
	cli.armPromptCacheKeepAlive("CLAUDEAI", "claude-fable-5-1", 0)
	ka := &cli.cacheKeepAlive
	ka.mu.Lock()
	gen := ka.gen
	ka.mu.Unlock()

	before := cli.costTracker.CacheStats().Requests
	if !cli.firePromptCacheKeepAlive(gen) || wc.calls != 1 {
		t.Fatalf("the current generation must refresh once, calls=%d", wc.calls)
	}
	if cli.costTracker.CacheStats().Requests != before+1 {
		t.Error("the refresh's cache read must be booked")
	}
	ka.mu.Lock()
	refreshes, newGen := ka.refreshes, ka.gen
	ka.mu.Unlock()
	if refreshes != 1 || newGen == gen {
		t.Fatalf("expected a re-arm with one refresh counted, got refreshes=%d gen %d→%d", refreshes, gen, newGen)
	}
	if cli.firePromptCacheKeepAlive(gen) {
		t.Fatal("a stale generation must not fire")
	}
	cli.armPromptCacheKeepAlive("CLAUDEAI", "claude-fable-5-1", keepAliveMaxRefreshes)
	ka.mu.Lock()
	gen = ka.gen
	ka.mu.Unlock()
	if cli.firePromptCacheKeepAlive(gen) || wc.calls != 1 {
		t.Fatal("past the idle stretch the entry is left to expire")
	}
	wc.err = llmclient.ErrPromptCacheKeepAliveUnsupported
	cli.armPromptCacheKeepAlive("CLAUDEAI", "claude-fable-5-1", 0)
	ka.mu.Lock()
	gen = ka.gen
	ka.mu.Unlock()
	if cli.firePromptCacheKeepAlive(gen) {
		t.Fatal("an unsupported client stops the schedule")
	}
	ka.mu.Lock()
	if ka.timer != nil {
		t.Error("cancel must drop the timer")
	}
	ka.mu.Unlock()
	var none *ChatCLI
	none.cancelPromptCacheKeepAlive()
	none.noteRealUsageForKeepAlive("x", "y")
}

// The lifetime "auto" settled on belongs to the conversation: a tenant
// swapped in starts with its own decision and the base set gets its own
// back when the tenant leaves.
func TestTenantSwapScopesTheHeldCacheTTL(t *testing.T) {
	cli := newTenantTestCLI(t)
	t.Setenv(llmclient.PromptCacheTTLEnv, "")
	llmclient.RestorePromptCacheTTL("1h")
	t.Cleanup(llmclient.ResetPromptCacheTTL)
	ctx := context.Background()

	leave := cli.enterTenant(ctx, "telegram:42")
	if leave == nil {
		t.Fatal("expected a tenant swap")
	}
	if held := llmclient.HeldPromptCacheTTL(); held != "" {
		t.Fatalf("a fresh tenant conversation must start undecided, got %q", held)
	}
	llmclient.SetPromptCacheTTLHint("5m")
	if got := llmclient.AnthropicCacheTTL(); got != "5m" {
		t.Fatalf("tenant resolved %q", got)
	}
	leave()
	if held := llmclient.HeldPromptCacheTTL(); held != "1h" {
		t.Fatalf("the base conversation must get its own decision back, got %q", held)
	}
	leave = cli.enterTenant(ctx, "telegram:42")
	if held := llmclient.HeldPromptCacheTTL(); held != "5m" {
		t.Fatalf("the tenant must get its own decision back, got %q", held)
	}
	leave()
}
