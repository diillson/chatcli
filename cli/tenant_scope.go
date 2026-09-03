/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Per-principal store sets for the gateway daemon.
 *
 * The gateway serves every channel identity through one ChatCLI. With
 * CHATCLI_HUB_ISOLATE=true the conversation hub already keeps one
 * conversation per principal, but every durable store — saved sessions,
 * long-term memory, knowledge contexts, the CCR archive, cost snapshots,
 * the transcript journal — and the live history were shared: a /session
 * list from one chat listed everyone's sessions, one user's facts surfaced
 * in another's turns. A tenant store set is a complete replacement of those
 * handles rooted at ~/.chatcli/tenants/<principal>; the gateway swaps it in
 * under its turn mutex before a principal's turn and swaps the base set
 * back after. Nothing changes for the single-user default: the swap only
 * happens when isolation is on and the principal is not the shared one.
 */
package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/cli/agent/workers"
	"github.com/diillson/chatcli/cli/compress"
	"github.com/diillson/chatcli/cli/workspace"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// GatewayMaxTenantsEnv caps how many tenant store sets stay resident;
// least recently used sets are released (their memory worker stopped)
// past the cap and rebuilt from disk on the next turn.
const GatewayMaxTenantsEnv = "CHATCLI_GATEWAY_MAX_TENANTS"

const defaultGatewayMaxTenants = 16

// tenantStores is one principal's complete store set plus its live
// conversation state.
type tenantStores struct {
	principal string
	root      string

	sessionManager   *SessionManager
	contextHandler   *ContextHandler
	memoryStore      *workspace.MemoryStore
	memWorker        *memoryWorker
	compressionLayer *compress.Layer
	costTracker      *CostTracker
	transcript       *transcriptJournal
	contextBuilder   *workspace.ContextBuilder

	history            []models.Message
	checkpoints        []conversationCheckpoint
	currentSessionName string
	boundSessionSync   time.Time
	boundRemoteOnly    bool

	graphWired bool
	lastUsed   time.Time
}

// tenantPool keeps the resident store sets and the base (single-user) set.
type tenantPool struct {
	mu     sync.Mutex
	max    int
	base   *tenantStores
	items  map[string]*tenantStores
	active string
}

// gatewayMaxTenants reads the residency cap.
func gatewayMaxTenants() int {
	if v := strings.TrimSpace(os.Getenv(GatewayMaxTenantsEnv)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultGatewayMaxTenants
}

// tenantRootFor derives the on-disk root for a principal: a readable,
// filesystem-safe slug plus a short digest so distinct principals that
// sanitize to the same slug never share a directory.
func tenantRootFor(base, principal string) string {
	slug := strings.ToLower(strings.TrimSpace(principal))
	var b strings.Builder
	for _, r := range slug {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	safe := b.String()
	if len(safe) > 48 {
		safe = safe[:48]
	}
	sum := sha256.Sum256([]byte(principal))
	return filepath.Join(base, "tenants", safe+"-"+hex.EncodeToString(sum[:4]))
}

// captureStores snapshots the ChatCLI's current store handles and live
// conversation state into a set (used for the base set and on leave).
func (cli *ChatCLI) captureStores(into *tenantStores) {
	into.sessionManager = cli.sessionManager
	into.contextHandler = cli.contextHandler
	into.memoryStore = cli.memoryStore
	into.memWorker = cli.memWorker
	into.compressionLayer = cli.compressionLayer
	into.costTracker = cli.costTracker
	into.transcript = cli.transcript
	into.contextBuilder = cli.contextBuilder
	into.history = cli.history
	into.checkpoints = cli.checkpoints
	into.currentSessionName = cli.currentSessionName
	into.boundSessionSync = cli.boundSessionSync
	into.boundRemoteOnly = cli.boundRemoteOnly
	into.root = cli.stateRoot
}

// applyStores installs a set on the ChatCLI and re-registers the process
// globals that bind a store handle at registration time (the CCR recaller
// and squad compression layer); the adapters that read cli fields at call
// time need nothing.
func (cli *ChatCLI) applyStores(ts *tenantStores) {
	cli.sessionManager = ts.sessionManager
	cli.contextHandler = ts.contextHandler
	cli.memoryStore = ts.memoryStore
	cli.memWorker = ts.memWorker
	cli.compressionLayer = ts.compressionLayer
	cli.costTracker = ts.costTracker
	cli.transcript = ts.transcript
	cli.contextBuilder = ts.contextBuilder
	cli.history = ts.history
	cli.checkpoints = ts.checkpoints
	cli.currentSessionName = ts.currentSessionName
	cli.boundSessionSync = ts.boundSessionSync
	cli.boundRemoteOnly = ts.boundRemoteOnly
	cli.stateRoot = ts.root
	if cli.historyCompactor != nil {
		cli.historyCompactor.SetCompressionLayer(ts.compressionLayer)
	}
	if ts.compressionLayer != nil {
		workers.RegisterCCRRecaller(ts.compressionLayer.Recall)
		workers.RegisterSquadCompressionLayer(ts.compressionLayer)
	}
	if ts.memoryStore != nil && !ts.graphWired {
		cli.wireMemoryGraph()
		ts.graphWired = true
	}
}

// buildTenantStores creates a principal's store set from disk.
func (cli *ChatCLI) buildTenantStores(ctx context.Context, principal string) (*tenantStores, error) {
	root := tenantRootFor(cli.stateRoot, principal)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	ts := &tenantStores{principal: principal, root: root, graphWired: false}

	sm, err := NewSessionManagerAt(filepath.Join(root, "sessions"), cli.logger)
	if err != nil {
		return nil, err
	}
	ts.sessionManager = sm

	ch, err := NewContextHandlerAt(filepath.Join(root, "contexts"), cli.logger)
	if err != nil {
		return nil, err
	}
	ch.GetManager().AttachEmbeddingProvider(cli.hydeProviderForSession())
	ts.contextHandler = ch

	if cli.memoryStore != nil { // memory enabled for the process
		ms := workspace.NewMemoryStore(root, cli.logger)
		ms.SetWorkspaceDir(cli.workspaceDir)
		ts.memoryStore = ms
		pool := cli.tenants
		ts.memWorker = newMemoryWorkerFor(cli, ms, filepath.Join(root, "memory", "pending"), func() []models.Message {
			pool.mu.Lock()
			active := pool.active == principal
			pool.mu.Unlock()
			if active {
				return cli.history
			}
			return ts.history
		})
		ts.memWorker.start(ctx)
	}

	layer := compress.NewLayerFromEnv(root)
	if err := layer.StoreFallback(); err != nil && cli.logger != nil {
		cli.logger.Warn("tenant CCR store unavailable; using bounded in-memory store", zap.String("principal", principal), zap.Error(err))
	}
	ts.compressionLayer = layer
	ts.costTracker = NewCostTrackerAt(filepath.Join(root, "costs"))
	if transcriptEnabled() {
		dir := filepath.Join(root, transcriptDirName)
		if err := os.MkdirAll(dir, 0o700); err == nil {
			if j, err := openTranscriptJournal(dir, newTranscriptID(time.Now())); err == nil {
				ts.transcript = j
			}
		}
	}
	ts.contextBuilder = workspace.NewContextBuilder(cli.bootstrapLoader, ts.memoryStore, cli.workspaceDir)
	ts.history = make([]models.Message, 0)
	return ts, nil
}

// enterTenant swaps principal's store set in and returns the function that
// swaps the base set back. Nil for the shared principal (no-op). Callers
// hold the gateway turn mutex, which is what makes the swap safe.
func (cli *ChatCLI) enterTenant(ctx context.Context, principal string) func() {
	if principal == "" || principal == defaultHubPrincipal {
		return nil
	}
	if cli.tenants == nil {
		cli.tenants = &tenantPool{max: gatewayMaxTenants(), items: map[string]*tenantStores{}}
		base := &tenantStores{principal: defaultHubPrincipal}
		cli.captureStores(base)
		cli.tenants.base = base
	}
	pool := cli.tenants
	pool.mu.Lock()
	ts, ok := pool.items[principal]
	pool.mu.Unlock()
	if !ok {
		built, err := cli.buildTenantStores(ctx, principal)
		if err != nil {
			if cli.logger != nil {
				cli.logger.Error("tenant store set unavailable; serving from the shared stores", zap.String("principal", principal), zap.Error(err))
			}
			return nil
		}
		ts = built
		ts.lastUsed = time.Now()
		pool.mu.Lock()
		pool.items[principal] = ts
		pool.active = principal // protects the newcomer from its own eviction pass
		cli.evictTenantsLocked()
		pool.mu.Unlock()
	}
	// Save the base set's live state (the REPL may be sharing this process)
	// and install the tenant.
	cli.captureStores(pool.base)
	pool.mu.Lock()
	pool.active = principal
	pool.mu.Unlock()
	ts.lastUsed = time.Now()
	cli.applyStores(ts)
	return func() {
		cli.captureStores(ts)
		ts.root = tenantRootFor(pool.base.root, principal)
		pool.mu.Lock()
		pool.active = ""
		pool.mu.Unlock()
		cli.applyStores(pool.base)
	}
}

// evictTenantsLocked releases least recently used sets beyond the cap.
// Caller holds pool.mu. The active set is never released.
func (cli *ChatCLI) evictTenantsLocked() {
	pool := cli.tenants
	for len(pool.items) > pool.max {
		var oldest *tenantStores
		for _, ts := range pool.items {
			if ts.principal == pool.active {
				continue
			}
			if oldest == nil || ts.lastUsed.Before(oldest.lastUsed) {
				oldest = ts
			}
		}
		if oldest == nil {
			return
		}
		if oldest.memWorker != nil {
			oldest.memWorker.stop()
		}
		delete(pool.items, oldest.principal)
	}
}

// enterGatewayTenant is the gateway hook: swaps the sender's store set in
// when hub isolation is on and the sender is not the shared principal.
func (cli *ChatCLI) enterGatewayTenant(ctx context.Context, sessions *hubSessions, platform, userID string) func() {
	if sessions == nil || sessions.store == nil || !resolveHubIsolate(ctx, sessions.store) {
		return nil
	}
	return cli.enterTenant(ctx, sessions.principalFor(ctx, platform, userID))
}

// activeTenant reports the principal whose store set is installed ("" for
// the base set).
func (cli *ChatCLI) activeTenant() string {
	if cli == nil || cli.tenants == nil {
		return ""
	}
	cli.tenants.mu.Lock()
	defer cli.tenants.mu.Unlock()
	return cli.tenants.active
}
