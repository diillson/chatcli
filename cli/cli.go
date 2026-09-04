/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Package cli implements the interactive terminal UI, command
// handlers, and orchestration glue that binds every chatcli
// subsystem together (LLM clients, workspace, memory, agents,
// plugins, MCP, hooks, quality pipeline).
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/cli/agent/lsp"
	"github.com/diillson/chatcli/cli/agent/proc"
	"github.com/diillson/chatcli/cli/agent/quality/lessonq"
	"github.com/diillson/chatcli/cli/agent/workers"
	"github.com/diillson/chatcli/cli/agentevents"
	"github.com/diillson/chatcli/cli/coder"
	"github.com/diillson/chatcli/cli/commands"
	"github.com/diillson/chatcli/cli/compress"
	"github.com/diillson/chatcli/cli/hooks"
	"github.com/diillson/chatcli/cli/mcp"
	"github.com/diillson/chatcli/cli/palette"
	"github.com/diillson/chatcli/cli/paste"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/cli/scheduler"
	"github.com/diillson/chatcli/cli/telemetry"
	"github.com/diillson/chatcli/cli/workspace"
	"github.com/diillson/chatcli/client/remote"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/manager"
	"github.com/diillson/chatcli/llm/openaiassistant"
	"github.com/diillson/chatcli/llm/tokenizer"
	"github.com/diillson/chatcli/pkg/browser"
	"github.com/diillson/chatcli/pkg/persona"
	"github.com/diillson/chatcli/ui/kit"
	"github.com/diillson/chatcli/ui/theme"
	"github.com/fsnotify/fsnotify"

	"github.com/diillson/chatcli/models"

	"go.uber.org/zap"
)

// Logger interface para facilitar a testabilidade
type Logger interface {
	Info(msg string, fields ...zap.Field)
	Error(msg string, fields ...zap.Field)
	Warn(msg string, fields ...zap.Field)
	Sync() error
}

// FileChunk representa um pedaço do conteúdo de arquivos
type FileChunk struct {
	Index   int
	Total   int
	Content string
}

// InteractionState tracks the current phase of the chat loop so the
// prompt prefix, signal handlers, and spinner can coordinate.
type InteractionState int

// ExecutionProfile selects which mode-specific defaults apply to the
// next LLM call (chat vs agent vs coder).
type ExecutionProfile int

// Interaction states and execution profiles (iota block shared for
// legacy reasons; the integer values never cross paths in use).
const (
	StateNormal InteractionState = iota
	StateSwitchingProvider
	StateProcessing
	StateAgentMode
	ProfileNormal ExecutionProfile = iota
	ProfileAgent
	ProfileCoder
)

// Sentinel errors used as panic values to unwind out of go-prompt's
// input loop when the user asks to enter agent/coder mode or exit.
var (
	errAgentModeRequest = errors.New("request to enter agent mode")
	errCoderModeRequest = errors.New("request to enter coder mode")
	errExitRequest      = errors.New("request to exit")
)

// CommandFlags declares the completer suggestions for the flag layer
// of every supported slash / @ command. See cli_completer.go for the
// top-level suggestion list.
var CommandFlags = map[string]map[string][]prompt.Suggest{
	"@file": {
		"--mode": {
			{Text: "full", Description: "Processa o conteúdo completo (padrão, trunca se necessário)"},
			{Text: "summary", Description: "Gera resumo estrutural (árvore de arquivos, tamanhos, sem conteúdo)"},
			{Text: "chunked", Description: "Divide grandes projetos em pedaços gerenciáveis (use /nextchunk para prosseguir)"},
			{Text: "smart", Description: "Seleciona arquivos relevantes com base no seu prompt (IA decide)"},
		},
	},
	"@command": {
		"-i":   {},
		"--ai": {},
	},
	"/session": {
		"new":    {},
		"save":   {},
		"load":   {},
		"list":   {},
		"delete": {},
	},
	"/context": {
		"create":   {},
		"attach":   {},
		"detach":   {},
		"list":     {},
		"delete":   {},
		"show":     {},
		"merge":    {},
		"attached": {},
		"export":   {},
		"import":   {},
		"metrics":  {},
		"help":     {},
	},
	"/connect": {
		"--token":          {},
		"--provider":       {},
		"--model":          {},
		"--llm-key":        {},
		"--use-local-auth": {},
		"--tls":            {},
		"--ca-cert":        {},
		"--client-id":      {},
		"--client-key":     {},
		"--realm":          {},
		"--agent-id":       {},
		"--ollama-url":     {},
	},
	"/agent": {
		"list":   {},
		"load":   {},
		"attach": {},
		"detach": {},
		"skills": {},
		"show": {
			{Text: "--full", Description: "Mostra detalhes completos do agente"},
		},
		"status": {},
		"off":    {},
		"help":   {},
	},
}

// ChatCLI representa a interface de linha de comando do chat
type ChatCLI struct {
	Client               client.LLMClient
	manager              manager.LLMManager
	logger               *zap.Logger
	Provider             string
	Model                string
	history              []models.Message  // unified conversation history (all modes)
	historyCompactor     *HistoryCompactor // manages history compaction
	proxyPayloadWarned   bool              // one-shot flag: user already warned about large history vs corporate proxy caps
	commandHistory       []string
	newCommandsInSession []string
	historyManager       *HistoryManager
	animation            *AnimationManager
	commandHandler       *CommandHandler
	lastCommandOutput    string
	fileChunks           []FileChunk // Chunks pendentes para processamento
	// pendingInboundImages stages vision attachments for the next coder/agent
	// run when they arrive from outside the interactive @file flow (e.g. the
	// messaging gateway). runCoderQuery merges and clears them. Transient.
	pendingInboundImages []models.ImageContent
	failedChunks         []FileChunk // Chunks que falharam no processamento
	lastFailedChunk      *FileChunk  // Referência ao último chunk que falhou
	agentMode            *AgentMode  // Modo de agente
	// taskGraphAdapter backs the @taskgraph builtin and /taskgraph; kept on
	// the struct so cleanup can cancel an in-flight graph run.
	taskGraphAdapter *taskGraphAdapter
	unattended       bool // when true, the agent runs without any interactive confirmation (gateway daemon)
	// policyAutoMode is the session-scoped /policy mode: when true, coder
	// policy "ask" verdicts auto-approve (deny rules, the command validator
	// and safety-immune operations still gate). Atomic because command
	// surfaces (REPL, ACP slash, MCP manage_session) may toggle it while an
	// agent loop reads it. Never persisted — new sessions start interactive.
	policyAutoMode atomic.Bool
	dangerBlock    bool   // when true (with unattended), dangerous commands are declined in-band instead of auto-approved (MCP server opt-in)
	lastAgentReply string // last one-shot agent prose answer (command blocks stripped), captured for unattended callers
	// agentEventSink, when set, receives structured events from the agent/
	// coder ReAct loop (ACP structured bridge). Installed per-run by
	// runLoopRPC under rpcStdoutSem and cleared on exit; never set by the
	// interactive REPL, gateway or scheduler paths.
	agentEventSink agentevents.Sink
	// rpcPermissions, when set, approves policy-gated actions through the
	// connected client even without an event sink (MCP elicitation bridge).
	// Installed per-run by runLoopRPC under rpcStdoutSem, like agentEventSink.
	rpcPermissions   agentevents.PermissionRequester
	interactionState InteractionState
	mu               sync.Mutex
	operationCancel  context.CancelFunc
	// sessionCtx is the process/session-lifetime context, seeded in
	// NewChatCLI and refreshed by Start. Interactive slash-command handlers
	// that have no per-request context of their own (e.g. /nextchunk) derive
	// their LLM-call deadline from it instead of context.Background().
	sessionCtx          context.Context
	isExecuting         atomic.Bool
	processingDone      chan struct{}
	messageQueue        []string   // FIFO queue of user messages typed during processing
	messageQueueMu      sync.Mutex // protects messageQueue
	prefixSpinnerIdx    int32      // atomic counter for animated prefix spinner
	sessionManager      *SessionManager
	currentSessionName  string
	boundSessionSync    time.Time // store mtime of the active named session as of our last load/save (cross-surface refresh watermark)
	boundRemoteOnly     bool      // active named session lives only on the remote server (user's explicit choice): local write-through/refresh stand down
	sessionAutosaved    bool      // autosave-on-exit ran (cleanup can be reached twice)
	UserMaxTokens       int
	pluginManager       *plugins.Manager
	contextHandler      *ContextHandler
	personaHandler      *PersonaHandler
	skillHandler        *SkillHandler
	executionProfile    ExecutionProfile
	pendingAction       string // stores intended action before panic (for Windows go-prompt tearDown workaround)
	replActive          bool   // true only inside the interactive Start() loop (gates the palette trigger)
	paletteRequested    bool   // a handler asked to open the command palette; the executor runs it in place
	paletteTarget       string // command to scope the palette to ("" opens the categorized root)
	suppressPaletteOnce bool   // skip the palette trigger for the next handled command (the palette's own selection)

	// Remote connection state (for /connect and /disconnect)
	localClient   client.LLMClient           // saved local client when connected to remote
	localProvider string                     // saved local provider name
	localModel    string                     // saved local model name
	remoteConn    interface{ Close() error } // remote connection (for cleanup on disconnect)
	isRemote      bool                       // true when connected to a remote server
	remoteAddress string                     // server address captured on /connect for /config display
	hubSync       *HubSync                   // cross-channel conversation sync (set on /connect or in local hub mode)
	hubLocalClose func()                     // closes the on-disk hub opened by local mode (nil otherwise)

	// K8s watcher context injection
	WatcherContextFunc func() string      // returns K8s context to prepend to LLM prompts
	isWatching         bool               // true when K8s watcher is active
	watchStatusFunc    func() string      // returns compact status for prompt prefix
	watcherCancel      context.CancelFunc // cancels the background watcher goroutine

	// Paste detection
	lastPasteInfo *paste.Info // set by BracketedPasteParser callback when paste is detected

	// Remote resource cache (populated on /connect, cleared on /disconnect)
	remoteAgents []remote.RemoteAgentInfo
	remoteSkills []remote.RemoteSkillInfo

	// Workspace context (bootstrap files + memory)
	contextBuilder *workspace.ContextBuilder
	memoryStore    *workspace.MemoryStore

	// Session-start snapshot of the persistent-memory bootstrap card
	// (memory_bootstrap.go). Computed once per process so the card stays
	// byte-stable across turns — it rides in the CACHED prefix of every
	// surface's system prompt, and a per-turn count drift would bust that
	// cache for no informational gain.
	bootstrapCard *bootstrapCardState // per store set: swapped with the tenant

	// Content-aware, reversible context compression (CCR). Reduces verbose
	// tool output (search/log/diff/JSON) before it reaches the model while
	// keeping the full original recoverable via @recall. Shared across modes.
	compressionLayer *compress.Layer
	// High-water mark of compression savings (bytes) already reported by the
	// per-turn telemetry, so each render shows only the delta since the last
	// one. Session totals live in /config compression stats.
	compressionSavedShown int64

	// LLM request audit trail writer (llm_audit.go); nil when the audit
	// path is not configured.
	llmAudit *llmAuditWriter

	// stateRoot is the directory the durable stores live under
	// (~/.chatcli by default; a tenant root while a per-principal store set
	// is active — see tenant_scope.go). bootstrapLoader and workspaceDir are
	// kept so tenant store sets can rebuild the context builder.
	stateRoot       string
	bootstrapLoader *workspace.BootstrapLoader
	workspaceDir    string
	tenants         *tenantPool

	// recalled tracks the facts auto-recall injected this turn so only
	// the ones the reply evidently used get reinforced.
	recalled recallEvidence

	// extToolCaller overrides the MCP manager for the extension points
	// (tests); extForward tracks what was forwarded to the memory provider.
	extToolCaller serverToolCaller
	extForward    *extForwardState // per store set: swapped with the tenant

	// Latest assembled system-prompt breakdown (chat and agent paths write
	// it, /context status reads it).
	promptBreakdowns promptBreakdownStore
	// Append-only transcript journal (transcript_journal.go): the durable
	// full record behind the compacted in-memory window.
	transcript *transcriptJournal

	// Session compaction controls: /autocompact threshold override and the
	// lazily resolved CHATCLI_COMPACT_MODEL summarizer (see compact_config.go).
	autoCompact       autoCompactControl
	compactSummarizer client.LLMClient
	// compactSummarizerProvider/Model name the summarizer's route so the
	// compactor can size its input against THAT model's window.
	compactSummarizerProvider string
	compactSummarizerModel    string
	// lastPromptChars is the character weight of the last chat request as
	// it went out (temp history + turn input): the calibrator's chars side.
	lastPromptChars int
	// calibrationTurns counts chat turns to pace exact (count_tokens)
	// calibration.
	calibrationTurns      int
	compactSummarizerOnce sync.Once

	// Session language-server pool behind the @lsp tool. Created lazily on
	// the first @lsp call (starting gopls for sessions that never navigate
	// code would be waste) and shut down with the session.
	lspPool     *lsp.Pool
	lspPoolOnce sync.Once

	// Session background-process supervisor behind the @proc tool. Created
	// lazily on the first @proc call; every process it owns dies with the
	// session.
	procSup     *proc.Supervisor
	procSupOnce sync.Once

	// Conversation checkpoints for rewind
	checkpoints []conversationCheckpoint
	// preCompaction is the undo stack for /rewind compact: the histories
	// the latest rewrites replaced (newest last).
	preCompaction [][]models.Message
	// lastRecallTrace is what the last auto-recall injected and why (/memory why).
	recallTraceMu   sync.Mutex
	lastRecallTrace *memoryRecallTrace
	// otlp is the OpenTelemetry metrics exporter (nil unless OTEL_* is set).
	otlp *telemetry.Exporter
	// pendingTurnContext is the chat turn's injected context text between
	// assembly and commit (turn_context.go).
	pendingTurnContext string
	// prefixRatios freezes the chars-per-token ratio the prefix budget uses
	// per provider:model for the session, so cached sections never fold or
	// unfold because the calibrator moved by a few percent (prompt_budget.go).
	prefixRatiosMu sync.Mutex
	prefixRatios   map[string]float64
	lastEscTime    time.Time // for Esc+Esc double-press detection

	// Background memory annotation worker
	memWorker        *memoryWorker
	sessionStartTime time.Time // for session duration tracking

	// Pending one-line memory notices produced by the background worker.
	// They are flushed to the terminal at the next executor tick so the
	// write never races with go-prompt's redraw (the worker runs on its
	// own goroutine and must not touch stdout directly).
	memNoticeMu sync.Mutex
	memNotices  []string

	// Pending update notice produced by backgroundUpdateFlow when the boot
	// refresh discovers a new release AFTER the welcome screen already
	// printed. Flushed at the next executor tick (same reason as memNotices:
	// never race with go-prompt's redraw). welcomeUpdateShown records the
	// version the welcome banner announced, so the mid-session notice never
	// repeats what the user just read.
	updateNoticeMu      sync.Mutex
	pendingUpdateNotice string
	welcomeUpdateShown  string

	// MCP (Model Context Protocol) servers for client mode
	mcpManager     *mcp.Manager
	mcpCancel      context.CancelFunc // cancel function for MCP server lifecycle
	mcpCtx         context.Context    // shared with mcpCancel; reused by hot-reload
	mcpConfigPath  string             // resolved path to mcp_servers.json
	mcpWatcher     *fsnotify.Watcher  // hot-reload watcher; nil when disabled
	mcpWatcherDone chan struct{}      // closed to stop the watcher loop
	mcpStartupDone chan struct{}      // closed once the initial StartAll pass finishes

	// MCP channel reactive triggers — engine, pending queues, and the
	// consumer goroutine that fans Actions out into them. Initialized
	// after mcpManager when MCP is enabled.
	channelTriggers *channelTriggerState

	// Hooks system for lifecycle events
	hookManager *hooks.Manager

	// Cost tracking for the current session
	costTracker *CostTracker
	// turnUsageRecorded marks that the current turn's usage already landed
	// in the tracker (the chat-ask/knowledge exception records per tool
	// round) so handleChatTurnResult must not record the turn a second
	// time. Set and cleared on the single-threaded REPL turn path.
	turnUsageRecorded bool

	// Cached provider models for autocomplete (populated asynchronously)
	cachedModels   []client.ModelInfo
	cachedModelsMu sync.RWMutex

	// AI-chosen route override ("PROVIDER:model") set by the @model tool.
	// Honored per agent/coder turn by clientAndCtxForTurn with priority over
	// skill model hints; cleared at every Run() start and by "@model reset".
	// Never mutates Client/Provider/Model — the user's session choice stays
	// authoritative outside the task.
	agentRouteOverride   string
	agentRouteOverrideMu sync.RWMutex

	// Multiline input buffer (--- delimiter toggle)
	multilineBuf MultilineBuffer

	// Pending manual skill invocation set by `/<skill-name>` routing.
	// Consumed (and cleared) by processLLMRequest to inject the skill
	// content into the system prompt for a single turn. See Fase 2 of the
	// advanced skill frontmatter support.
	pendingManualSkill     *persona.Skill
	pendingManualSkillArgs string

	// Slash command catalog (.chatcli/commands + interop dirs) and the
	// single-turn hints staged by an expansion: model/effort route the
	// turn (same plumbing as manual-skill hints), allowed-tools becomes an
	// ephemeral security-gate overlay on the next agent/coder run.
	slashCommands              *commands.Catalog
	pendingCommandModel        string
	pendingCommandEffort       string
	pendingCommandAllowedTools []string

	// pendingCoderCommandInput holds the RAW "/name args" invocation of a
	// mode:coder slash command captured by the chat auto-route. Consumed
	// exactly once by the Start dispatcher after the panic-unwind, which
	// expands it there (not before: pre-exec approval prompts need the
	// terminal in cooked mode, and the staged hints must land immediately
	// before the AgentMode.Run that consumes them).
	pendingCoderCommandInput string

	// Session-level reasoning override set by /thinking. When override.set
	// is true the value of override.effort wins over skill hints and
	// per-agent defaults for the next chat-turn LLM call. EffortUnset
	// inside an active override means "thinking explicitly off".
	thinkingOverride thinkingOverrideState

	// One-shot flag set by /plan that forces Plan-First on the next
	// /agent or /coder run regardless of complexity heuristics. Cleared
	// by AgentMode.runPlanFirstIfApplicable after the plan executes.
	pendingPlanFirst bool

	// /plan preview (or /plan dry): when true, only the planner runs and
	// its output is rendered — no execution, no orchestrator. Consumed
	// together with pendingPlanFirst in runPlanFirstIfApplicable.
	pendingPlanDryRun bool

	// planDryRunHandled is set by runPlanFirstIfApplicable after rendering
	// the dry-run preview. AgentMode.Run checks this to skip the ReAct
	// loop so we don't burn a SendPrompt call on a request the user only
	// wanted to preview.
	planDryRunHandled bool

	// /refine and /verify session toggles. nil pointers mean "no
	// override — defer to /config quality"; *bool=true forces the
	// hook on, *bool=false forces it off. Consumed by AgentMode
	// when it builds qualityConfig.
	qualityOverrides qualityOverridesState

	// reflexionRunner is the durable lesson queue (WAL + worker pool +
	// DLQ) used by ReflexionHook in enterprise mode. Lazily constructed
	// on first use by ensureReflexionRunner(); shut down by cleanup().
	// nil means reflexion falls back to the legacy detached-goroutine
	// path (non-durable, lessons lost on crash).
	reflexionRunner   *lessonq.Runner
	reflexionRunnerMu sync.Mutex

	// Scheduler subsystem — /schedule, /wait, /jobs. One of scheduler
	// or schedulerRemote is populated after initScheduler; never both.
	// A nil pair means the scheduler is disabled for this session
	// (CHATCLI_SCHEDULER_ENABLED=false or init failed).
	scheduler       *scheduler.Scheduler
	schedulerRemote *scheduler.RemoteClient
	schedulerSocket string
	schedulerCancel context.CancelFunc
	schedulerDirty  int32 // atomic — bumped by scheduler events for prompt refresh
	// schedulerBridge is the in-process CLIBridge implementation. The
	// agent loop reaches it via ChatCLI when handling @park parking
	// (snapshot enqueue) and resume notifications. Nil when the
	// scheduler subsystem is disabled or running remote-only.
	schedulerBridge *schedulerBridge

	// pendingResumeQueue holds park tokens whose resume jobs fired
	// while the user was at the chat prompt. The outer Run() loop
	// dequeues them before re-entering the prompt. Guarded by
	// pendingResumeMu — the bridge writes from the scheduler
	// dispatcher goroutine; cli.go reads from the main loop.
	pendingResumeMu    sync.Mutex
	pendingResumeQueue []string

	// parkOutcomes carries the (outcome, detail) tuple from the
	// NotifyParkComplete call to the consumer that runs RunResumed,
	// without forcing the consumer to re-read scheduler state. The map
	// is keyed by token; entries are deleted on consumption.
	parkOutcomeMu sync.Mutex
	parkOutcomes  map[string]parkOutcome

	// parkWaiters holds the in-turn wake channels registered by parks taken
	// on unattended surfaces (ACP / MCP server / gateway), where no REPL
	// loop exists to drain pendingResumeQueue. The bridge delivers the wake
	// directly to the blocked turn instead of queueing it. Keyed by token;
	// channels are buffered(1) so the scheduler dispatcher never blocks.
	parkWaiterMu sync.Mutex
	parkWaiters  map[string]chan parkOutcome

	// recentlyResumedTokens tracks tokens that drainPendingResumes
	// just consumed, so the auto-injected "/resume <token>" command
	// (fired by NotifyParkComplete via TTY inject) can short-circuit
	// when it lands a moment later. Without this, handleResumeCommand
	// would re-resolve the same token, find the snapshot already
	// deleted, and surface a confusing "snapshot not found" error to
	// the user. Entries TTL out after 30 s — long enough to cover any
	// scheduling pause between drain and the slash command, short
	// enough to never hide a legitimate stale-token error.
	recentlyResumedMu     sync.Mutex
	recentlyResumedTokens map[string]time.Time

	// activeParks tracks parks created by THIS session that are still
	// waiting for their resume. While one exists, plain text typed at the
	// prompt is captured as a directive for the parked agent (persisted
	// into its snapshot) instead of being routed to chat mode — where the
	// agent would never see it and the model answers with tool_call XML
	// that chat never executes. Ordered oldest→newest; guarded by
	// activeParkMu (the bridge and the REPL touch it from different
	// goroutines).
	activeParkMu sync.Mutex
	activeParks  []activePark
}

// activePark is one waiting park owned by this session. ResumeAtDisplay
// is the preformatted wallclock shown in the /park-note notice; HintShown
// keeps the discoverability hint to one line per park.
type activePark struct {
	Token           string
	ResumeAtDisplay string
	HintShown       bool
}

// parkOutcome carries the resume-time payload from the bridge's
// NotifyParkComplete to the cli.go consumer. Kept tiny and value-typed
// so the map stays cheap to copy.
type parkOutcome struct {
	Outcome string
	Detail  string
}

// thinkingOverrideState carries the user's /thinking choice. set
// distinguishes "no override" (auto behavior) from "explicit off"
// (effort = EffortUnset, skip the hint).
type thinkingOverrideState struct {
	set    bool
	effort client.SkillEffort
}

// NewChatCLI cria uma nova instância de ChatCLI
func NewChatCLI(ctx context.Context, manager manager.LLMManager, logger *zap.Logger) (*ChatCLI, error) {
	cli := &ChatCLI{
		manager:          manager,
		logger:           logger,
		history:          make([]models.Message, 0),
		historyCompactor: NewHistoryCompactor(logger),
		historyManager:   NewHistoryManager(logger),
		animation:        NewAnimationManager(),
		interactionState: StateNormal,
		processingDone:   make(chan struct{}),
		executionProfile: ProfileNormal,
	}

	// Seed the session-lifetime context so interactive handlers can derive
	// deadlines from it before Start refreshes it. Every caller passes a
	// non-nil context (a real request ctx or context.Background()).
	cli.sessionCtx = ctx

	// Wire the security prompt's package-level input guard with our zap
	// logger so dropped typeahead is logged at DEBUG. Calling this here
	// (before any tool can fire a confirmation) guarantees every prompt in
	// the process lifetime uses a logger-aware guard.
	coder.SetSecurityPromptLogger(logger)

	// Initialize the per-session scratch workspace. This makes
	// $CHATCLI_AGENT_TMPDIR available to every tool and registers the
	// scratch + tool-results dirs with the engine / read validator so the
	// agent can read and write inside them.
	if _, err := agent.InitSessionWorkspace(logger); err != nil {
		// Fatal in the sense of "the sandbox is broken" — but we only log
		// and continue. The agent falls back to the old /tmp/chatcli-*
		// shared dirs without read-on-demand access, which is the legacy
		// behavior.
		logger.Warn("Failed to initialize session workspace — large tool outputs will not be readable", zap.Error(err))
	}

	pluginMgr, err := plugins.NewManager(logger)
	if err != nil {
		// Logamos o erro, mas a aplicação continua. O pluginManager será um objeto válido, mas vazio.
		logger.Error("Falha crítica ao inicializar o gerenciador de plugins, plugins estarão desabilitados", zap.Error(err))
	}
	cli.pluginManager = pluginMgr
	if pluginMgr != nil {
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinCoderPlugin())
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinWebFetchPlugin())
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinWebSearchPlugin())
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinSchedulerPlugin())
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinParkPlugin())
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinAskPlugin())
		// @commands — model-facing discovery/expansion of the slash-command
		// template catalog (the user-facing side is the /name dispatch).
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinCommandsPlugin())
		// @send — proactive outbound messaging through the gateway
		// platform adapters (Telegram/WhatsApp/Discord/Slack/webhook).
		// The adapter is wired below; gateway.BuildConfigured() reads the
		// live platform credentials each call, so this works whether or
		// not the gateway daemon is running.
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinSendPlugin())
		// @moa — Mixture-of-Agents: fan a prompt out to several models and
		// synthesize one best answer. Wired below to the LLM manager.
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinMoaPlugin())
		// @model — model/provider routing by the AI itself: list providers
		// and models (tier/price/context/capabilities), switch the rest of
		// the task to a fitter model, or delegate a self-contained subtask
		// to a cheaper one. Adapter wired below over the LLM manager.
		// CHATCLI_AGENT_MODEL_TOOL=false disables it (cost governance).
		if isModelToolEnabled() {
			pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinModelPlugin())
		}
		// @osv — keyless dependency vulnerability scanning via OSV.dev.
		// Self-contained (HTTP + filesystem), no adapter wiring needed.
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinOsvPlugin())
		// @session — search past saved conversations. Wired below to the
		// SessionManager.
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinSessionPlugin())
		// @speak — text-to-speech to an audio file (local/keyless-first).
		// Self-contained via llm/tts; no adapter wiring needed.
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinSpeakPlugin())
		// @voice — per-conversation voice reply control for the messaging
		// gateway: the model calls it when the user asks to start/stop
		// receiving audio answers. Backed by the shared preference store.
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinVoicePlugin(nil))
		// @image — text-to-image generation to a file (self-hosted/keyless
		// first via Stable Diffusion WebUI). Self-contained via llm/imagegen.
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinImagePlugin())
		// @embed — ad-hoc semantic text operations (similarity, ranking,
		// raw vectors) over the configured embedding backend. Persistent
		// RAG stays with @context/@knowledge. Self-contained via
		// llm/embedding; no adapter wiring needed.
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinEmbedPlugin())
		// @skill — self-authoring skills: the agent writes/evolves its own
		// skills from what it learns, persisted to the global skills dir and
		// auto-activated in future sessions. Self-contained.
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinSkillPlugin())
		// @memory — deterministic long-term memory writes/recall. The
		// adapter is wired below once the memory store exists; until then
		// the tool reports "memory not enabled" rather than panicking.
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinMemoryPlugin())
		// @knowledge — pull-side of /context --mode knowledge: search, read
		// and walk attached documentation corpora on demand. Adapter wired
		// below once the context handler exists.
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinKnowledgePlugin())
		// @lsp — semantic code navigation through real language servers:
		// diagnostics, definition, references, symbols, hover. Adapter wired
		// below over a lazily created, session-scoped server pool.
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinLSPPlugin())
		// @mcp-login — authorize an OAuth-protected remote MCP server so its
		// tools become usable. The agent invokes it after an mcp_* call fails
		// with an "authorization required" error. Adapter wired below over the
		// live MCP manager.
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinMCPLoginPlugin())
		// @tools — tool-catalog meta-tool behind the deferred catalog: the
		// model pulls full definitions of indexed tools on demand instead of
		// every definition riding in every prompt. Adapter wired below.
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinToolsPlugin())
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinChannelsPlugin())
		// @proc — background-process supervision: start/status/logs/stop for
		// long-running commands (dev servers, watchers). Adapter wired below
		// over a session supervisor sharing the agent exec validator.
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinProcPlugin())
		// @http — structured HTTP client for testing APIs (pairs with @proc:
		// start the server, hit its endpoints). Self-contained: shares the
		// hardened proxy/TLS/SSRF web client with @webfetch.
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinHTTPPlugin())
		// @api-explorer — end-to-end API reconnaissance: from a base URL it
		// auto-discovers the OpenAPI/Swagger spec (JSON or YAML) across ~18
		// well-known locations, fingerprints server tech/auth/rate-limit from
		// response headers, catalogs every path/method/parameter/security
		// scheme, deep-dives a single endpoint, and introspects GraphQL
		// schemas. Read-only (GET/HEAD/OPTIONS + GraphQL introspection).
		// Self-contained: shares the hardened proxy/TLS/SSRF web client.
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinAPIExplorerPlugin())
		// @browser — drive a real local Chrome/Chromium over the DevTools
		// protocol: open, snapshot (numbered interactive elements), click,
		// type, eval, screenshot, console and network capture. The web
		// verification loop. Self-contained: speaks CDP directly over the
		// websocket client already shipped — no driver, no new dependency;
		// requires a locally installed Chromium-family browser.
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinBrowserPlugin())
		// @view — attach a local image to the conversation through the
		// vision pipeline (native multimodal or describe-fallback), so the
		// agent can LOOK at screenshots, mocks and diagrams mid-task.
		// Adapter wired below over the session vision pipeline.
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinViewPlugin())
		// @forge — pull requests, issues and CI through the user's own
		// authenticated gh/glab CLI (keyless: no stored credentials).
		// Reads are auto-approved; pr-create/comments hit the security gate.
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinForgePlugin())
		// @docs-flatten — push-side companion of @knowledge: flattens a
		// Markdown/MDX docs tree (local dir or git repo) into the JSONL
		// corpus /context --mode knowledge ingests. Self-contained.
		// @context — autonomous knowledge-base management: the agent builds,
		// attaches/detaches and inspects its own context/knowledge bases without
		// the user running /context. Adapter wired below.
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinContextPlugin())
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinDocsFlattenPlugin())
		// @registry-tags — keyless container image tag discovery across
		// public/private OCI registries (Docker Hub, GCR, GHCR, Quay, ACR,
		// Harbor, Artifactory). Reads ~/.docker/config.json for private
		// repos and performs the OCI Bearer-token dance for anonymous ones.
		// Self-contained (HTTP only), no adapter wiring needed.
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinRegistryTagsPlugin())
		// @wikipedia — keyless factual lookup via the public MediaWiki API
		// (search titles / read an article intro), language-configurable.
		// A companion to @websearch/@webfetch/@knowledge. Self-contained.
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinWikipediaPlugin())
		// @diagram — render architecture/dependency/flow diagrams to
		// PNG/SVG/JPG from Graphviz DOT, with crisp and exactly-correct text
		// labels. Graphviz is embedded as WASM (go-graphviz + wazero): no cgo,
		// no external `dot` to install, no network. Also builds the real Go
		// import graph of a module via `go list`. Self-contained.
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinDiagramPlugin())
		// @graphview — render an INTERACTIVE, Obsidian-style force-directed
		// graph (drag/zoom/pan/search/filter) to a self-contained HTML file
		// and open it in the browser. The physics engine is embedded JS on a
		// canvas: no CDN, no network, no API key. Sources: json (model-
		// supplied nodes/edges — e.g. the conversation), knowledge (the
		// in-core knowledge graph) and conversation (a structural session
		// graph). Provider for the latter two wired below.
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinGraphViewPlugin())
		// @compress / @recall — content-aware, reversible context compression
		// (CCR). @compress shrinks bulky payloads (logs/search/diff/JSON) on
		// demand; @recall restores the byte-identical original from a
		// "<<ccr:KEY>>" marker. The same layer also compresses tool output
		// automatically in the agent/coder loop. Adapter wired below once the
		// compression layer exists.
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinCompressPlugin())
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinRecallPlugin())
		// Atomic read-only tools (Claude Code parity, Item 1). Narrow,
		// flat-schema tools that route into the same engine as @coder
		// read/search/tree but give the LLM a dedicated entry point —
		// the model picks @read instead of remembering the @coder
		// envelope, and the orchestrator can fan them out in parallel
		// because each declares IsConcurrencySafe.
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinReadPlugin())
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinSearchPlugin())
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinTreePlugin())

		// @todo (Claude Code TodoWrite parity, Item 2). Lets the LLM
		// own its plan: write the full list each turn (canonical TodoWrite
		// semantics), list the current state, or mark a single item by
		// id. Adapter wired below routes into the live AgentMode's
		// TaskTracker.
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinTodoPlugin())

		// @agents — squad observability: lets the LLM list live agent runs
		// (workers, subagents, MoA members, scheduler headless), inspect
		// per-run progress and cancel a stuck run. Adapter wired below over
		// the process-wide run registry.
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinAgentsPlugin())

		// @board — the squad work board: the orchestrator LLM breaks goals
		// into cards, assigns worker agent types, moves them across the
		// kanban and records review/delivery notes. Humans watch via /board.
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinBoardPlugin())

		// @mail — squad messaging for the orchestrator: message workers
		// (delivered on their next turn), drain its own inbox, audit
		// traffic. Workers use their native send_mail tool; humans /mail.
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinMailPlugin())

		// @taskgraph — DAG orchestration for approved multi-task plans:
		// parallel squad workers per task, validation gates run by the
		// engine itself and an independent reviewer verdict before any
		// task counts as done. Adapter wired below over the live
		// dispatcher; humans watch via /taskgraph.
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinTaskGraphPlugin())

		// Slash-as-tool: register the curated subset of slash commands
		// (currently /help and /version) as plugins so the LLM can invoke
		// them via the same native tool dispatch path used by @coder,
		// @websearch, etc. The bindings live in slash_tool_registry.go +
		// slash_tool_handlers.go; expanding the set is a deliberate
		// review-gated decision (see registerBuiltinSlashTools doc).
		cli.registerBuiltinSlashTools()
		for _, entry := range AllSlashTools() {
			if plugin := NewSlashToolPlugin(entry); plugin != nil {
				pluginMgr.RegisterBuiltinPlugin(plugin)
			}
		}
	}

	cli.configureProviderAndModel()

	client, err := manager.GetClient(cli.Provider, cli.Model)
	if err != nil {
		// If the configured provider is not available, try to pick any available one
		available := manager.GetAvailableProviders()
		if len(available) > 0 {
			cli.Provider = available[0]
			cli.Model = ""
			client, err = manager.GetClient(cli.Provider, cli.Model)
		}
		if err != nil {
			// No provider available at all — start in auth-only mode
			logger.Warn("Nenhum provedor LLM disponível. Use /auth login para autenticar.", zap.Error(err))
			client = nil
		}
	}

	sessionMgr, err := NewSessionManager(logger)
	if err != nil {
		return nil, fmt.Errorf("erro ao inicializar o SessionManager: %w", err)
	}
	cli.sessionManager = sessionMgr
	cli.currentSessionName = ""

	contextHandler, err := NewContextHandler(logger)
	if err != nil {
		return nil, fmt.Errorf("erro ao inicializar o ContextHandler: %w", err)
	}
	cli.contextHandler = contextHandler
	// Watcher-driven context refreshes print at the REPL tick, never from
	// the watcher goroutine.
	cli.contextHandler.SetRefreshNotifier(cli.pushMemoryNotice)
	// Wire the shared embedding provider so `/context attach --rag` can retrieve
	// only the passages relevant to each turn instead of dumping whole files. A
	// Null/absent provider (no CHATCLI_EMBED_PROVIDER) leaves retrieval disabled,
	// and --rag attachments transparently fall back to whole-content injection.
	cli.contextHandler.GetManager().AttachEmbeddingProvider(cli.hydeProviderForSession())
	cli.attachKnowledgeReranker(cli.contextHandler.GetManager())

	// Initialize workspace context (bootstrap files + memory)
	homeDir, _ := os.UserHomeDir()
	globalDir := filepath.Join(homeDir, ".chatcli")
	cli.stateRoot = globalDir
	cli.installTokenEstimator()
	workspaceDir := detectProjectDir()
	if workspaceDir == "" {
		workspaceDir, _ = os.Getwd()
	}

	// CHATCLI_BOOTSTRAP_DIR overrides the global bootstrap directory
	if envDir := os.Getenv("CHATCLI_BOOTSTRAP_DIR"); envDir != "" {
		globalDir = envDir
	}

	bootstrapEnabled := os.Getenv("CHATCLI_BOOTSTRAP_ENABLED") != "false"
	memoryEnabled := os.Getenv("CHATCLI_MEMORY_ENABLED") != "false"

	var bootstrapLoader *workspace.BootstrapLoader
	if bootstrapEnabled {
		bootstrapLoader = workspace.NewBootstrapLoader(workspaceDir, globalDir, logger)
		if cwd, err := os.Getwd(); err == nil {
			bootstrapLoader.SetWorkingDir(cwd)
		}
	} else {
		bootstrapLoader = workspace.NewBootstrapLoader("", "", logger) // noop
		logger.Info("Bootstrap disabled via CHATCLI_BOOTSTRAP_ENABLED=false")
	}

	var memStore *workspace.MemoryStore
	if memoryEnabled {
		memDir := filepath.Join(homeDir, ".chatcli")
		memStore = workspace.NewMemoryStore(memDir, logger)
	} else {
		logger.Info("Memory disabled via CHATCLI_MEMORY_ENABLED=false")
	}
	cli.memoryStore = memStore
	if memStore != nil {
		memStore.SetWorkspaceDir(workspaceDir)
		// Wire the @memory tool to this session's store so the agent can
		// persist facts deterministically.
		plugins.SetMemoryAdapter(&memoryPluginAdapter{cli: cli})
		// Persisted knowledge-graph cache: adopt graph.json when current,
		// rebuild on store mutations (see wireMemoryGraph).
		cli.wireMemoryGraph()
	}

	// Build the content-aware compression layer (CCR store under ~/.chatcli/ccr)
	// from environment config, and wire the @compress/@recall tools to it. The
	// layer is also consulted by the agent/coder loop to compress tool output.
	cli.initTranscriptJournal("")
	cli.initLLMAudit("repl")
	cli.initTelemetry(ctx, "repl")
	cli.initCacheResourceCosting()
	// GPT models count tokens locally: warm the vocabulary in the
	// background so the first exact count is ready by the first turn.
	if tokenizer.IsGPTModel(cli.Model) {
		tokenizer.Prefetch(cli.Model)
	}
	cli.compressionLayer = compress.NewLayerFromEnv(filepath.Join(homeDir, ".chatcli"))
	if err := cli.compressionLayer.StoreFallback(); err != nil {
		// The layer already degraded to a bounded in-memory store; surface the
		// cause so the loss of cross-restart recall is never silent.
		logger.Warn("CCR persistent store unavailable; compression running on a bounded in-memory store (offloaded originals will not survive a restart)",
			zap.Error(err))
	}
	plugins.SetCompressionAdapter(&compressionPluginAdapter{cli: cli})
	// Let the history compactor reduce oversized tool feedback / injected
	// context reversibly (CCR) instead of byte-truncating — engages during
	// compaction across agent, coder and chat.
	if cli.historyCompactor != nil {
		cli.historyCompactor.SetCompressionLayer(cli.compressionLayer)
	}
	// Share the same compression engine with delegated sub-agents/workers so
	// their tool output is compressed too, and identical content read by
	// sibling agents dedupes against the one content-addressed CCR store.
	// Workers share the same secret-redaction chokepoint as the
	// orchestrator (redaction first, then reversible compression).
	workers.RegisterToolOutputCompressor(func(toolName, output string) string {
		out, _ := cli.compressionLayer.CompressToolOutput(toolName, redactSecretsForLLM(output))
		return out
	})
	// The reverse direction: workers get a recall tool over the same store,
	// so compression is no longer a one-way door inside a worker's loop.
	workers.RegisterCCRRecaller(cli.compressionLayer.Recall)
	// Worker-loop microcompact archives dropped bytes into the same store.
	workers.RegisterSquadCompressionLayer(cli.compressionLayer)
	// Read-only memory/session/knowledge views for workers that opt in
	// (persona frontmatter tools: Memory, Session, Knowledge).
	workers.RegisterContextToolRunner(cli.runWorkerContextTool)
	// Per-task plugin grants: workers can be granted session plugins
	// (@browser, @websearch, mcp_*) opt-in per task; these wire the executor
	// and the grant-name → native-def translator.
	workers.RegisterPluginToolRunner(cli.runWorkerPluginTool)
	workers.RegisterPluginToolDefiner(cli.workerPluginToolDefs)

	// Wire the @knowledge tool to this session's context manager so the agent
	// can interrogate attached knowledge bases on demand.
	plugins.SetKnowledgeAdapter(&knowledgePluginAdapter{cli: cli})
	// Wire the @lsp tool to the session language-server pool (created lazily
	// on first use; shut down with the session).
	plugins.SetLSPAdapter(&lspToolAdapter{cli: cli})
	// Wire the @mcp-login tool to the live MCP manager (OAuth authorization).
	plugins.SetMCPAuthAdapter(&mcpLoginAdapter{cli: cli})
	// Wire the @tools meta-tool to the plugin registry (deferred catalog).
	plugins.SetToolCatalogAdapter(&toolCatalogPluginAdapter{cli: cli})
	plugins.SetChannelsAdapter(&channelsPluginAdapter{cli: cli})
	// Wire the @proc tool to the session process supervisor (created lazily
	// on first use; all processes die with the session).
	plugins.SetProcAdapter(&procToolAdapter{cli: cli})

	// Wire the @view tool to the session vision pipeline (image loading,
	// compression, native-vs-describe gating and agent-loop staging).
	plugins.SetViewAdapter(&viewToolAdapter{cli: cli})
	// @context adapter — lets the agent create/attach/detach/inspect its own
	// context bases over the same live manager.
	plugins.SetContextAdapter(&contextPluginAdapter{cli: cli})
	// @graphview provider — feeds the knowledge/conversation sources from this
	// session's in-core knowledge graph and message history.
	plugins.SetGraphSourceProvider(&graphViewPluginAdapter{cli: cli})

	// Wire the @send tool to the gateway platform registry. Independent of
	// the memory store and of the gateway daemon lifecycle: adapters are
	// (re)built from live credentials on each invocation.
	plugins.SetSendAdapter(&sendPluginAdapter{cli: cli})

	// Wire the @moa (Mixture-of-Agents) tool to the LLM manager so it can fan
	// prompts across the configured providers and synthesize a best answer.
	plugins.SetMoaAdapter(&moaPluginAdapter{cli: cli})

	// Wire the @model tool to the live session (manager + catalog + cost
	// tables + agent-loop route override).
	plugins.SetModelRoutingAdapter(&modelRoutingAdapter{cli: cli})

	// Wire the @session tool to the saved-session store so the agent can
	// search past conversations.
	plugins.SetSessionAdapter(&sessionPluginAdapter{cli: cli})
	cli.contextBuilder = workspace.NewContextBuilder(bootstrapLoader, memStore, workspaceDir)
	cli.bootstrapLoader = bootstrapLoader
	cli.workspaceDir = workspaceDir

	// Start background memory annotation worker
	if memoryEnabled {
		cli.memWorker = newMemoryWorker(cli)
		cli.memWorker.start(ctx)
		// Record session start for usage pattern tracking
		cli.sessionStartTime = time.Now()
		if mgr := memStore.Manager(); mgr != nil {
			mgr.Patterns.RecordSessionStart()
		}
	}

	// Initialize hooks system
	cli.hookManager = hooks.NewManager(logger)
	cli.hookManager.LoadFromSettings()

	// Initialize cost tracker
	cli.costTracker = NewCostTracker()

	cli.bootstrapMCP(ctx, logger)

	// Initialize persona handler
	cli.personaHandler = NewPersonaHandler(logger)

	// Set project directory for local agents/skills precedence
	if projectDir := detectProjectDir(); projectDir != "" {
		cli.personaHandler.GetManager().SetProjectDir(projectDir)
		logger.Debug("Project directory set for persona", zap.String("dir", projectDir))
	}

	// Initialize skill registry handler
	cli.skillHandler = NewSkillHandler(logger, cli.personaHandler.GetManager())

	cli.Client = client
	cli.commandHandler = NewCommandHandler(cli)
	// Slash command catalog: needs the command handler (reserved names
	// derive from its live route table) and the project dir.
	cli.initSlashCommands(detectProjectDir())
	cli.registerCommandPaletteProvider()
	plugins.SetCommandsAdapter(&commandsPluginAdapter{cli: cli})
	cli.agentMode = NewAgentMode(cli, logger)

	// Wire the @todo plugin adapter (Item 2). The getter returns the
	// CURRENT agentMode's tracker on every call so re-creations of the
	// AgentMode (line 1066 etc.) are transparent — the plugin sees the
	// most recent instance, never a stale pointer.
	plugins.SetTodoAdapter(newLiveTodoAdapter(func() *agent.TaskTracker {
		if cli.agentMode == nil {
			return nil
		}
		return cli.agentMode.taskTracker
	}))

	// Wire the @agents plugin adapter over the process-wide run registry.
	plugins.SetAgentsAdapter(newLiveAgentsAdapter(nil))

	// Wire the @board plugin adapter over the squad board store.
	boardAdapter := newLiveBoardAdapter(nil)
	boardAdapter.onMutate = cli.markGraphDirty
	plugins.SetBoardAdapter(boardAdapter)

	// Wire the @mail plugin adapter over the squad message bus.
	plugins.SetMailAdapter(newLiveMailAdapter(nil))

	// Wire the @taskgraph plugin adapter. It resolves the CURRENT
	// AgentMode's dispatcher at call time (the AgentMode is re-created
	// across sessions), so wiring once at boot is safe.
	cli.taskGraphAdapter = newTaskGraphAdapter(cli, cli.logger)
	plugins.SetTaskGraphAdapter(cli.taskGraphAdapter)

	// Wire the policy_manager's capability resolver (Item 4). When a
	// tool call hits no explicit policy rule AND the plugin advertises
	// IsReadOnly for those args, auto-allow instead of defaulting to
	// ActionAsk. @websearch, @webfetch GET, @read, @search, @tree,
	// @scheduler query/list, @coder read/search/tree all benefit.
	coder.SetPluginCapabilityResolver(func(toolName, rawArgs string) coder.PluginCapabilityResult {
		if cli.pluginManager == nil {
			return coder.PluginCapabilityResult{}
		}
		plugin, ok := cli.pluginManager.GetPlugin(toolName)
		if !ok || plugin == nil {
			return coder.PluginCapabilityResult{}
		}
		// The resolver feeds the args as a single-element slice (the JSON
		// envelope) so the plugin's IsReadOnly helper sees the same shape
		// it would see at Execute time.
		args := []string{rawArgs}
		return coder.PluginCapabilityResult{
			Known:    true,
			ReadOnly: plugins.IsReadOnly(plugin, args),
		}
	})

	history, err := cli.historyManager.LoadHistory()
	if err != nil {
		cli.logger.Error("Erro ao carregar o histórico", zap.Error(err))
	} else {
		cli.commandHistory = history
	}

	// Pre-fetch available models for autocomplete
	cli.refreshModelCache(ctx)

	// Fire SessionStart hook
	if cli.hookManager != nil {
		wd, _ := os.Getwd()
		cli.hookManager.FireAsync(ctx, hooks.HookEvent{
			Type:       hooks.EventSessionStart,
			Timestamp:  time.Now(),
			SessionID:  cli.currentSessionName,
			WorkingDir: wd,
		})
	}

	// Initialize scheduler subsystem (in-process or daemon client).
	cli.initScheduler(ctx)

	return cli, nil
}

// detectProjectDir walks up from the current working directory looking for
// project root markers. Returns the project root path, or "" if none found.
// Priority: .agent (explicit ChatCLI marker) > .git (common convention).
func detectProjectDir() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if info, err := os.Stat(filepath.Join(dir, ".agent")); err == nil && info.IsDir() {
			return dir
		}
		if info, err := os.Stat(filepath.Join(dir, ".git")); err == nil && info.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

func (cli *ChatCLI) executor(in string) {
	in = strings.TrimSpace(in)

	// Drain any pending park resumes BEFORE processing the user's
	// command. Slash commands like /parked, /jobs and chat input all
	// route through this executor without ever exiting p.Run(); without
	// this hook a resume that fires during interactive use would only
	// trigger when the user later runs /coder or /agent. The drain runs
	// foreground and may take a while (it re-enters the agent loop) —
	// that is exactly the desired UX: the user sees the agent continue,
	// then their original input is processed afterwards.
	if cli.drainPendingResumes(context.Background()) {
		// Flush any cosmetic state the resume left behind.
		_ = os.Stdout.Sync()
	}

	// Auto-triggered agent runs queued by the MCP channel trigger
	// engine fire here, BEFORE the user's command. Symmetric with
	// park resume — the user sees the agent investigate the event,
	// then their typed input is processed. Notify/confirm banners
	// are rendered separately below so they appear even on turns
	// that have no queued auto-trigger.
	if cli.drainPendingAutoTriggers(cli.sessionCtx) {
		_ = os.Stdout.Sync()
	}
	cli.renderChannelTriggerBanner()

	// Flush any memory notices the background worker produced since the
	// last tick, so "memory updated" feedback is visible instead of silent.
	cli.drainMemoryNotices()

	// Announce a new release the boot-time refresh discovered after the
	// welcome screen had already printed — without this the user only
	// learns about it on the NEXT boot.
	cli.drainUpdateNotice()

	// Handle paste: replace placeholder with real content and show notification
	if cli.lastPasteInfo != nil {
		info := cli.lastPasteInfo
		cli.lastPasteInfo = nil
		// If a placeholder was used (large paste), swap it for the real content
		if info.Placeholder != "" && strings.Contains(in, info.Placeholder) {
			in = strings.Replace(in, info.Placeholder, info.Content, 1)
		}
		if info.LineCount > 1 {
			fmt.Printf("  %s\n", i18n.T("paste.detected", info.CharCount, info.LineCount))
		} else {
			fmt.Printf("  %s\n", i18n.T("paste.detected.short", info.CharCount))
		}
	}

	// Multiline input: accumulate lines between --- delimiters.
	// While active, each Enter adds a line instead of submitting.
	wasActive := cli.multilineBuf.Active()
	complete, fullText := cli.multilineBuf.ProcessLine(in)
	if !complete {
		if !wasActive {
			// Just entered multiline mode — show hint (plain text, go-prompt can't render ANSI)
			fmt.Printf("  %s\n", i18n.T("multiline.hint", cli.multilineBuf.Delimiter()))
		}
		return // still accumulating — changeLivePrefix shows "... [N]"
	}
	// Use the assembled text (single-line returns as-is; multiline returns joined)
	in = fullText

	if in != "" {
		cli.commandHistory = append(cli.commandHistory, in)
		cli.newCommandsInSession = append(cli.newCommandsInSession, in)
	}

	if strings.HasPrefix(in, "/run") {
		// Guard against entering agent mode with an empty task — that
		// would ship an empty user message to the LLM. Mirrors the
		// /agent fix: show a usage hint and stay in chat mode.
		task := strings.TrimSpace(strings.TrimPrefix(in, "/run"))
		if task == "" {
			fmt.Println(colorize("  "+i18n.T("agent.usage.hint"), ColorYellow))
			return
		}
		// Smart-routing: trivial conversational queries may be answered by
		// a single chat-mode turn instead of spinning up the agent loop.
		// In default "hint" mode this only prints a tip and falls through.
		// In "auto" mode the chat path is invoked and we return here.
		if cli.maybeReroute(context.Background(), "/run", task) {
			return
		}
		cli.pendingAction = "agent"
		panic(errAgentModeRequest)
	}

	if in == "" {
		return
	}

	if cli.interactionState == StateSwitchingProvider {
		cli.handleProviderSelection(in)
		cli.interactionState = StateNormal
		return
	}

	if strings.Contains(strings.ToLower(in), "@command ") {
		command := strings.TrimPrefix(in, "@command ")
		cli.executeDirectCommand(command)
		return
	}

	// /park-note <msg>: explicit directive for the parked agent. Plain
	// text keeps its normal chat behavior even while a park is waiting —
	// directing the agent is an opt-in action, not a mode change.
	if in == "/park-note" || strings.HasPrefix(in, "/park-note ") {
		cli.handleParkNoteCommand(strings.TrimSpace(strings.TrimPrefix(in, "/park-note")))
		return
	}

	if strings.HasPrefix(in, "/") || in == "exit" || in == "quit" {
		exit := cli.commandHandler.HandleCommand(context.Background(), in)
		if !exit {
			// If the handler asked to open the command palette, run it now —
			// raw mode is already released while the executor runs — and
			// execute whatever the user selects.
			exit = cli.runRequestedPalette()
		}
		if exit {
			cli.pendingAction = "exit"
			panic(errExitRequest)
		}
		return
	}

	if cli.Client == nil {
		fmt.Println(i18n.T("cli.error.no_provider_configured"))
		return
	}

	// Discoverability hint (once per park): plain chat still works while
	// an agent is parked, but the user may actually be talking TO the
	// parked agent — surface /park-note before the chat turn runs.
	cli.maybeShowParkNoteHint()

	// If already processing, queue the message for later (type-ahead)
	if cli.isExecuting.Load() {
		cli.messageQueueMu.Lock()
		if len(cli.messageQueue) < 10 {
			cli.messageQueue = append(cli.messageQueue, in)
			queueLen := len(cli.messageQueue)
			cli.messageQueueMu.Unlock()
			fmt.Printf("\n%s\n", kit.Notice(kit.LevelInfo, i18n.T("queue.message_queued", queueLen)))
		} else {
			cli.messageQueueMu.Unlock()
			fmt.Printf("\n%s\n", kit.Notice(kit.LevelWarn, i18n.T("queue.full")))
		}
		return
	}

	cli.interactionState = StateProcessing
	// Async on every platform: go-prompt keeps reading while the turn runs,
	// which is what makes the type-ahead queue and the prefix spinner work.
	// The post-turn prompt redraw is driven by forceRefreshPrompt — SIGWINCH
	// self-signal on Unix, injected no-op key event on Windows.
	go cli.processLLMRequest(context.Background(), in)
}

// IsExecuting retorna true se uma operação está em andamento
func (cli *ChatCLI) IsExecuting() bool {
	return cli.isExecuting.Load()
}

// CancelOperation cancela a operação atual se houver uma
func (cli *ChatCLI) CancelOperation() {
	cli.mu.Lock()
	defer cli.mu.Unlock()

	if cli.operationCancel != nil {
		cli.operationCancel()
	}
}

// dequeueMessage removes and returns the first message from the queue.
// Returns "" if the queue is empty.
func (cli *ChatCLI) dequeueMessage() string {
	cli.messageQueueMu.Lock()
	defer cli.messageQueueMu.Unlock()

	if len(cli.messageQueue) == 0 {
		return ""
	}

	msg := cli.messageQueue[0]
	cli.messageQueue = cli.messageQueue[1:]
	return msg
}

func (cli *ChatCLI) Start(ctx context.Context) {
	cli.sessionCtx = ctx
	defer cli.cleanup(ctx)
	// Bounded session lifecycle: expire MACHINE-created sessions (REPL
	// autosaves, MCP session mirrors) past their TTL, in the background so
	// boot never waits on disk. User-named sessions are never touched.
	if cli.sessionManager != nil {
		go cli.sessionManager.CleanExpiredMachineSessions()
	}
	// Same TTL for the stores that never expired on their own (park
	// snapshots, cost snapshots); see retention.go and /config retention.
	go cli.runRetentionPass()
	// Cache de release vencido ganha um refresh síncrono de orçamento curto
	// para a PRÓPRIA welcome anunciar release nova de imediato; o fluxo em
	// background cobre o timeout (aviso drenado no turno seguinte), renova o
	// cache que alimenta o hash de commit de builds go install e, com
	// CHATCLI_AUTO_UPDATE=auto, aplica o update silencioso por staging.
	cli.preWelcomeUpdateCheck(ctx)
	go cli.backgroundUpdateFlow(ctx)
	cli.PrintWelcomeScreen()
	cli.printLastSessionNotice()
	cli.startHubSync(ctx) // resume the shared cross-channel conversation, if connected

	// Mark the interactive REPL as active so the command-palette trigger only
	// fires here — never when HandleCommand is driven headless (scheduler,
	// gateway, one-shot), where unwinding the prompt has no meaning.
	cli.replActive = true
	defer func() { cli.replActive = false }()

	shouldContinue := true
	for shouldContinue {
		func() {
			defer func() {
				if r := recover(); r != nil {
					// On Windows, go-prompt tearDown may panic with "close of closed channel"
					// which replaces our original panic value. Use pendingAction as fallback.
					action := cli.pendingAction
					cli.pendingAction = ""
					rErr, _ := r.(error)
					switch {
					case errors.Is(rErr, errAgentModeRequest) || action == "agent":
						// agent mode switch — nothing to undo here.
					case errors.Is(rErr, errCoderModeRequest) || action == "coder":
						cli.restoreTerminal()
					case errors.Is(rErr, errExitRequest) || action == "exit":
						shouldContinue = false
					default:
						panic(r)
					}
				}
			}()

			// On Windows, go-tty turns AltGr characters ("/" on ABNT2, "@"
			// on German, …) into ESC+rune, which go-prompt would insert
			// verbatim into the buffer as a stray glyph. Sanitize before the
			// paste detector sees the stream. See wininput.go.
			var inputParser prompt.ConsoleParser = prompt.NewStandardInputParser()
			if runtime.GOOS == "windows" {
				inputParser = newAltGrParser(inputParser)
			}
			pasteParser := paste.NewBracketedPasteParser(
				inputParser,
				func(info paste.Info) {
					cli.lastPasteInfo = &info
				},
			)

			p := prompt.New(
				cli.executor,
				cli.completer,
				prompt.OptionParser(pasteParser),
				prompt.OptionTitle("ChatCLI - LLM no seu Terminal"),
				prompt.OptionLivePrefix(cli.changeLivePrefix),
				prompt.OptionPrefixTextColor(prompt.Green),
				prompt.OptionInputTextColor(prompt.White),
				prompt.OptionSuggestionBGColor(prompt.DarkGray),
				prompt.OptionDescriptionBGColor(prompt.Black),
				prompt.OptionSuggestionTextColor(prompt.White),
				prompt.OptionDescriptionTextColor(prompt.Yellow),
				prompt.OptionSelectedSuggestionBGColor(prompt.Blue),
				prompt.OptionSelectedDescriptionBGColor(prompt.DarkGray),
				prompt.OptionHistory(cli.commandHistory),
				prompt.OptionMaxSuggestion(10),
				prompt.OptionAddKeyBind(prompt.KeyBind{
					Key: prompt.ControlC,
					Fn:  cli.handleCtrlC,
				}),
				// Esc: double-press detection for rewind
				prompt.OptionAddKeyBind(prompt.KeyBind{
					Key: prompt.Escape,
					Fn:  cli.handleEscape,
				}),
				// Ctrl+Arrow: word navigation (for terminals that send xterm sequences)
				prompt.OptionAddKeyBind(prompt.KeyBind{
					Key: prompt.ControlRight,
					Fn:  prompt.GoRightWord,
				}),
				prompt.OptionAddKeyBind(prompt.KeyBind{
					Key: prompt.ControlLeft,
					Fn:  prompt.GoLeftWord,
				}),
				// Shift+Arrow: character navigation (no selection in go-prompt)
				prompt.OptionAddKeyBind(prompt.KeyBind{
					Key: prompt.ShiftRight,
					Fn:  prompt.GoRightChar,
				}),
				prompt.OptionAddKeyBind(prompt.KeyBind{
					Key: prompt.ShiftLeft,
					Fn:  prompt.GoLeftChar,
				}),
				// Terminal escape sequences for modifier+arrow combinations.
				// Many terminals send non-standard CSI sequences that go-prompt
				// doesn't recognize, causing raw escape bytes to appear as text.
				// We register all known variants via ASCIICodeBind.
				prompt.OptionAddASCIICodeBind(
					// ── Alt/Option + Arrow (word navigation) ──────────────
					// CSI: ESC [ 1 ; 3 C / D (macOS Terminal, iTerm2, most xterm-like)
					prompt.ASCIICodeBind{
						ASCIICode: []byte{0x1b, 0x5b, 0x31, 0x3b, 0x33, 0x43},
						Fn:        prompt.GoRightWord,
					},
					prompt.ASCIICodeBind{
						ASCIICode: []byte{0x1b, 0x5b, 0x31, 0x3b, 0x33, 0x44},
						Fn:        prompt.GoLeftWord,
					},
					// Meta: ESC f / ESC b (iTerm2 "Natural Text Editing", readline convention)
					prompt.ASCIICodeBind{
						ASCIICode: []byte{0x1b, 0x66},
						Fn:        prompt.GoRightWord,
					},
					prompt.ASCIICodeBind{
						ASCIICode: []byte{0x1b, 0x62},
						Fn:        prompt.GoLeftWord,
					},

					// ── Ctrl + Arrow (word navigation) ────────────────────
					// CSI: ESC [ 1 ; 5 C / D (xterm, most modern terminals)
					prompt.ASCIICodeBind{
						ASCIICode: []byte{0x1b, 0x5b, 0x31, 0x3b, 0x35, 0x43},
						Fn:        prompt.GoRightWord,
					},
					prompt.ASCIICodeBind{
						ASCIICode: []byte{0x1b, 0x5b, 0x31, 0x3b, 0x35, 0x44},
						Fn:        prompt.GoLeftWord,
					},
					// macOS Terminal: ESC ESC [ C / D (double-ESC variant)
					prompt.ASCIICodeBind{
						ASCIICode: []byte{0x1b, 0x1b, 0x5b, 0x43},
						Fn:        prompt.GoRightWord,
					},
					prompt.ASCIICodeBind{
						ASCIICode: []byte{0x1b, 0x1b, 0x5b, 0x44},
						Fn:        prompt.GoLeftWord,
					},
					// rxvt: ESC O c / d
					prompt.ASCIICodeBind{
						ASCIICode: []byte{0x1b, 0x4f, 0x63},
						Fn:        prompt.GoRightWord,
					},
					prompt.ASCIICodeBind{
						ASCIICode: []byte{0x1b, 0x4f, 0x64},
						Fn:        prompt.GoLeftWord,
					},

					// ── Cmd + Arrow (line beginning/end — macOS) ──────────
					// ESC [ H (Home) / ESC [ F (End) — xterm
					prompt.ASCIICodeBind{
						ASCIICode: []byte{0x1b, 0x5b, 0x48},
						Fn:        prompt.GoLineBeginning,
					},
					prompt.ASCIICodeBind{
						ASCIICode: []byte{0x1b, 0x5b, 0x46},
						Fn:        prompt.GoLineEnd,
					},
					// ESC O H (Home) / ESC O F (End) — application mode
					prompt.ASCIICodeBind{
						ASCIICode: []byte{0x1b, 0x4f, 0x48},
						Fn:        prompt.GoLineBeginning,
					},
					prompt.ASCIICodeBind{
						ASCIICode: []byte{0x1b, 0x4f, 0x46},
						Fn:        prompt.GoLineEnd,
					},
					// ESC [ 1 ~ (Home) / ESC [ 4 ~ (End) — vt100/linux console
					prompt.ASCIICodeBind{
						ASCIICode: []byte{0x1b, 0x5b, 0x31, 0x7e},
						Fn:        prompt.GoLineBeginning,
					},
					prompt.ASCIICodeBind{
						ASCIICode: []byte{0x1b, 0x5b, 0x34, 0x7e},
						Fn:        prompt.GoLineEnd,
					},

					// ── Shift + Arrow (character navigation) ──────────────
					// CSI: ESC [ 1 ; 2 C / D
					prompt.ASCIICodeBind{
						ASCIICode: []byte{0x1b, 0x5b, 0x31, 0x3b, 0x32, 0x43},
						Fn:        prompt.GoRightChar,
					},
					prompt.ASCIICodeBind{
						ASCIICode: []byte{0x1b, 0x5b, 0x31, 0x3b, 0x32, 0x44},
						Fn:        prompt.GoLeftChar,
					},

					// ── Alt + Backspace (delete word backward) ────────────
					// ESC + DEL (0x7f) — most terminals
					prompt.ASCIICodeBind{
						ASCIICode: []byte{0x1b, 0x7f},
						Fn:        prompt.DeleteWord,
					},
					// ESC + BS (0x08) — some terminals
					prompt.ASCIICodeBind{
						ASCIICode: []byte{0x1b, 0x08},
						Fn:        prompt.DeleteWord,
					},
				),
			)

			p.Run()
			shouldContinue = false
		}()

		if shouldContinue {
			cli.restoreTerminal()

			lastCmd := ""
			if len(cli.commandHistory) > 0 {
				lastCmd = cli.commandHistory[len(cli.commandHistory)-1]
			}

			// /plan coder <task> routes to coder; /plan [agent] <task> routes to agent.
			planCoder := strings.HasPrefix(lastCmd, "/plan coder ") || lastCmd == "/plan coder"
			if cli.pendingCoderCommandInput != "" {
				// A mode:coder slash command auto-routed from chat: the
				// task lives in the pending field, not in commandHistory.
				cli.runPendingCoderCommand(ctx)
			} else if strings.HasPrefix(lastCmd, "/coder") || planCoder {
				cli.runCoderLogic(ctx)
			} else if strings.HasPrefix(lastCmd, "/run") || strings.HasPrefix(lastCmd, "/agent") || strings.HasPrefix(lastCmd, "/plan") {
				cli.runAgentLogic(ctx)
			}

			// Auto-resume any parked agents whose scheduler-driven wait
			// has been satisfied since the user was last at the prompt.
			// The drain runs AFTER the foreground dispatch returns so a
			// resume cannot interrupt active agent work; it runs BEFORE
			// the next prompt iteration so the user sees their parked
			// agent continue without manual intervention.
			//
			// Drain is iterative because a resumed agent may itself emit
			// a new @park, which queues the next token while we're still
			// processing the previous one — keep draining until the
			// queue is empty for one full cycle.
			for cli.drainPendingResumes(ctx) {
				// loop body intentionally empty; drainPendingResumes
				// returns false when the queue is exhausted.
			}
		}
	}
}

// runRequestedPalette runs the command palette when a handler flagged it, then
// executes the user's selection in place. It is called from the executor,
// where go-prompt has already torn down raw mode, so the alt-screen overlay can
// own the terminal and hand it back cleanly. Returns true when the selection
// asks to exit the REPL. A no-op outside an interactive terminal.
func (cli *ChatCLI) runRequestedPalette() bool {
	if !cli.paletteRequested {
		return false
	}
	cli.paletteRequested = false
	target := cli.paletteTarget
	cli.paletteTarget = ""
	if !theme.ActiveProfile().IsTerminal() {
		return false
	}
	var m palette.Model
	if target == "" {
		m = palette.NewRoot(cli.paletteSuggest)
	} else {
		m = palette.NewScoped(cli.paletteSuggest, target)
	}
	// go-prompt's BreakLine committed the trigger line ("❯ /") to the main
	// buffer right before this runs. On Windows the alt-screen exit does not
	// reconcile the main buffer the way xterm does, so that line survives as
	// a ghost above the palette result — erase it before the overlay opens.
	// The cursor sits at column 0 of the line just below it.
	if runtime.GOOS == "windows" {
		fmt.Print("\033[A\033[2K\r")
	}
	sel, err := palette.Run(context.Background(), m)
	if err != nil {
		cli.logger.Warn("command palette failed", zap.Error(err))
		return false
	}
	if sel == "" {
		return false // canceled
	}
	// Execute the selection as if typed. suppressPaletteOnce stops a bare,
	// pickable selection (e.g. "/config", "/switch") from reopening the
	// overlay; the trigger consumes and clears it on this very call.
	cli.suppressPaletteOnce = true
	exit := cli.commandHandler.HandleCommand(context.Background(), sel)
	cli.suppressPaletteOnce = false
	cli.paletteRequested = false // ignore any re-trigger raised during execution
	return exit
}

func (cli *ChatCLI) restoreTerminal() {
	if runtime.GOOS == "windows" {
		cmd := exec.Command("cmd", "/c", "cls") // Limpa a tela no Windows
		cmd.Stdout = os.Stdout
		if err := cmd.Run(); err != nil {
			cli.logger.Warn("Falha ao tentar limpar a tela do terminal no Windows", zap.Error(err))
		}
		return
	}
	cmd := exec.Command("stty", "sane")
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		cli.logger.Warn("Falha ao restaurar o terminal com 'stty sane'", zap.Error(err))
	}
	fmt.Print("\033[2J\033[H")
}

// Helper para executar lógica do agente com cancelamento via Ctrl+C
func (cli *ChatCLI) runWithCancellation(parent context.Context, taskName string, fn func(context.Context) error) {
	// Cria contexto cancelável derivado do contexto pai
	ctx, cancel := context.WithCancel(parent)

	// Registra o cancelamento na struct para acesso global se necessário
	cli.mu.Lock()
	cli.operationCancel = cancel
	cli.isExecuting.Store(true) // Marca que estamos executando algo
	cli.mu.Unlock()

	// Canal para capturar Ctrl+C
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Goroutine para vigiar o sinal
	go func() {
		select {
		case <-sigChan:
			fmt.Println(colorize("\n\n"+i18n.T("cli.signal.interrupt"), ColorRed))
			cancel() // Cancela o contexto, matando LLM e Plugins
		case <-ctx.Done():
			// A tarefa terminou normalmente, paramos de ouvir
		}
	}()

	defer func() {
		signal.Stop(sigChan) // Limpa o hook do sinal
		cancel()             // Garante limpeza do contexto
		cli.mu.Lock()
		cli.operationCancel = nil
		cli.isExecuting.Store(false)
		cli.mu.Unlock()
	}()

	// Executa a função do agente
	err := fn(ctx)

	// Tratamento de erro específico para cancelamento
	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Println(colorize("\n "+i18n.T("cli.status.operation_canceled_returning"), ColorYellow))
		} else {
			fmt.Println(colorize("\n "+i18n.T("cli.error.execution_failed", err), ColorRed))
		}
	}
}

func (cli *ChatCLI) runAgentLogic(ctx context.Context) {
	cli.setExecutionProfile(ProfileAgent)
	defer cli.setExecutionProfile(ProfileNormal)

	if len(cli.commandHistory) == 0 {
		return
	}
	lastCommand := cli.commandHistory[len(cli.commandHistory)-1]

	query := ""
	if strings.HasPrefix(lastCommand, "/agent") {
		query = strings.TrimSpace(strings.TrimPrefix(lastCommand, "/agent"))
	} else if strings.HasPrefix(lastCommand, "/run") {
		query = strings.TrimSpace(strings.TrimPrefix(lastCommand, "/run"))
	} else if strings.HasPrefix(lastCommand, "/plan") {
		query = strings.TrimSpace(strings.TrimPrefix(lastCommand, "/plan"))
		// Optional subcommands after /plan
		for _, sub := range []string{"preview", "dry", "agent"} {
			if strings.HasPrefix(query, sub+" ") || query == sub {
				query = strings.TrimSpace(strings.TrimPrefix(query, sub))
				break
			}
		}
	} else {
		fmt.Println(i18n.T("error.agent_query_extraction"))
		return
	}
	if query == "" {
		// Defense-in-depth: the /agent and /run entry points already
		// refuse empty inputs, but if some upstream change bypasses
		// them we still don't want to ship an empty user message to
		// the LLM.
		fmt.Println(colorize("  "+i18n.T("agent.usage.hint"), ColorYellow))
		return
	}

	rendererAgent := agent.NewUIRenderer(cli.logger)
	rendererAgent.RenderModeBanner("🤖", i18n.T("agent.banner.title"), agent.ColorLime, [][2]string{
		{i18n.T("agent.banner.task"), query},
		{i18n.T("agent.banner.mode"), i18n.T("agent.banner.mode_value")},
	})

	query, additionalContext, images := cli.processSpecialCommands(ctx, query)
	images, visionDesc := cli.gateImagesForModel(ctx, images)
	additionalContext += visionDesc

	if cli.agentMode == nil {
		cli.agentMode = NewAgentMode(cli, cli.logger)
	}

	cli.agentMode.pendingUserImages = images
	cli.agentMode.isOneShot = false // interactive /agent: keep the loop conversational
	cli.runWithCancellation(ctx, "Agent Mode", func(ctx context.Context) error {
		return cli.agentMode.Run(ctx, query, additionalContext, "")
	})

	// Nudge memory worker after agent run
	if cli.memWorker != nil {
		cli.memWorker.nudge(ctx)
	}

	fmt.Println(i18n.T("status.agent_mode_exit"))
}

func (cli *ChatCLI) runCoderLogic(ctx context.Context) {
	cli.setExecutionProfile(ProfileCoder)
	defer cli.setExecutionProfile(ProfileNormal)

	if len(cli.commandHistory) == 0 {
		return
	}
	lastCommand := cli.commandHistory[len(cli.commandHistory)-1]

	var query string
	if strings.HasPrefix(lastCommand, "/plan coder") {
		query = strings.TrimSpace(strings.TrimPrefix(lastCommand, "/plan coder"))
	} else {
		query = strings.TrimSpace(strings.TrimPrefix(lastCommand, "/coder"))
	}
	if query == "" {
		fmt.Println(i18n.T("error.agent_query_extraction"))
		return
	}

	wd, _ := os.Getwd()
	renderer := agent.NewUIRenderer(cli.logger)
	// "🛠️" carries the VS-16 emoji-presentation selector so runewidth and the
	// terminal agree it is 2 cells wide. The bare "🛠" (U+1F6E0) defaults to
	// text presentation and is measured as 1 while most terminals render it as
	// 2, which drifted the banner's top border. (No trailing-space hack now.)
	renderer.RenderModeBanner("🛠️", i18n.T("coder.banner.title"), agent.ColorCyan, [][2]string{
		{i18n.T("coder.banner.objective"), query},
		{i18n.T("coder.banner.workspace"), wd},
		{i18n.T("coder.banner.policy"), i18n.T("coder.banner.policy_value")},
	})

	query, additionalContext, images := cli.processSpecialCommands(ctx, query)
	images, visionDesc := cli.gateImagesForModel(ctx, images)
	additionalContext += visionDesc

	if cli.agentMode == nil {
		cli.agentMode = NewAgentMode(cli, cli.logger)
	}

	cli.agentMode.pendingUserImages = images
	cli.agentMode.isOneShot = false // interactive /coder: wait for input between turns
	cli.runWithCancellation(ctx, "Coder Mode", func(ctx context.Context) error {
		return cli.agentMode.Run(ctx, query, additionalContext, CoderSystemPrompt)
	})

	// Nudge memory worker after coder run
	if cli.memWorker != nil {
		cli.memWorker.nudge(ctx)
	}

	fmt.Println(colorize("\n "+i18n.T("coder.session_finished"), ColorGreen))
}

func (cli *ChatCLI) handleCtrlC(buf *prompt.Buffer) {
	// Cancel multiline mode on Ctrl+C
	if cli.multilineBuf.Active() {
		cli.multilineBuf.Reset()
		buf.DeleteBeforeCursor(len([]rune(buf.Text())))
		fmt.Printf("\n  [%s]\n", i18n.T("multiline.canceled"))
		return
	}

	if cli.isExecuting.Load() {
		// Clear queued messages first
		cli.messageQueueMu.Lock()
		queueLen := len(cli.messageQueue)
		cli.messageQueue = cli.messageQueue[:0]
		cli.messageQueueMu.Unlock()

		if queueLen > 0 {
			fmt.Printf("\n  %s", colorize(i18n.T("queue.messages_removed", queueLen), ColorYellow))
		}

		fmt.Println(i18n.T("prompt.cancel_op"))

		cli.mu.Lock()
		if cli.operationCancel != nil {
			cli.operationCancel()
		}
		cli.mu.Unlock()

		cli.interactionState = StateNormal

		cli.forceRefreshPrompt()

	} else {
		fmt.Println(i18n.T("prompt.confirm_exit"))
		cli.cleanup(context.Background())
		os.Exit(0)
	}
}

// handleEscape detects Esc+Esc double-press for rewind.
// Only triggers when the input buffer is empty.
func (cli *ChatCLI) handleEscape(buf *prompt.Buffer) {
	// Only trigger on empty input
	if buf.Text() != "" {
		return
	}

	now := time.Now()
	if now.Sub(cli.lastEscTime) < 500*time.Millisecond {
		// Double Esc detected
		cli.lastEscTime = time.Time{}
		cli.showRewindMenu()
		cli.forceRefreshPrompt()
		return
	}
	cli.lastEscTime = now
}

func (cli *ChatCLI) changeLivePrefix() (string, bool) {
	// Show continuation prompt while accumulating multiline input.
	// go-prompt does not render ANSI escape codes in the prefix — use plain text.
	if cli.multilineBuf.Active() {
		return fmt.Sprintf("  ... [%d] ", cli.multilineBuf.LineCount()+1), true
	}

	switch cli.interactionState {
	case StateSwitchingProvider:
		return i18n.T("prompt.select_provider"), true
	case StateProcessing:
		idx := atomic.LoadInt32(&cli.prefixSpinnerIdx)
		// Shared braille frames (same set as the "thinking" animation) so the
		// two spinners look identical. The glyph is left UNCOLORED on purpose:
		// this string is go-prompt's live prefix, and go-prompt measures its
		// width by counting runes — embedding ANSI escapes here would throw
		// off the cursor column. Color lives only in surfaces we render
		// ourselves (the thinking animation, cards).
		s := spinnerFrames[int(idx)%len(spinnerFrames)]
		if cli.Client != nil {
			modelName := cli.Client.GetModelName()
			cli.messageQueueMu.Lock()
			queueLen := len(cli.messageQueue)
			cli.messageQueueMu.Unlock()
			if queueLen > 0 {
				return fmt.Sprintf("[%s %s • %d na fila] ❯ ", modelName, s, queueLen), true
			}
			return fmt.Sprintf("[%s %s] ❯ ", modelName, s), true
		}
		return fmt.Sprintf("[processando %s] ❯ ", s), true
	case StateAgentMode:
		return "", true
	default:
		// Single grouped badge holds all the status icons that used to
		// pile up as separate `[remote]`, `[watch]`, `[jobs: 2▶ 1⏳]`,
		// `[🅿️ resume: 1]` prefixes. Empty when no badge is active so
		// the bare `❯` stays minimal.
		var icons []string
		if cli.isRemote {
			icons = append(icons, "🌐")
		}
		if cli.isWatching {
			icons = append(icons, "⏵")
		}
		if badge := cli.schedulerStatusLine(); badge != "" {
			// Schedulerline already comes formatted as "[jobs: 2▶ 1⏳]";
			// strip the wrapping brackets so the grouped badge stays
			// consistent ("▶2⏳1" is enough inside the unified [..]).
			trimmed := strings.TrimSuffix(strings.TrimPrefix(badge, "["), "]")
			trimmed = strings.TrimPrefix(trimmed, "jobs:")
			trimmed = strings.TrimSpace(trimmed)
			if trimmed != "" {
				icons = append(icons, trimmed)
			}
		}
		// Park resume badge: alerts the user that a parked agent has a
		// resume queued and is waiting for the next executor tick to
		// drain. Without this, the user might keep idle-pressing Enter
		// and never see the resume fire (empty Enter doesn't reach the
		// executor in go-prompt). Typing any command — including a
		// no-op like a single character — drains the queue.
		cli.pendingResumeMu.Lock()
		n := len(cli.pendingResumeQueue)
		cli.pendingResumeMu.Unlock()
		if n > 0 {
			icons = append(icons, fmt.Sprintf("🅿%d", n))
		}

		var prefix string
		if len(icons) > 0 {
			prefix = "[" + strings.Join(icons, " ") + "] "
		}
		if cli.currentSessionName != "" {
			prefix += cli.currentSessionName + " "
		}
		prefix += "❯ "
		return prefix, true
	}
}

// schedulerStatusLine returns a short badge like "[jobs: 2▶ 1⏳]"
// suitable for the prompt prefix. Empty string when nothing is active.
func (cli *ChatCLI) schedulerStatusLine() string {
	if !cli.schedulerEnabled() {
		return ""
	}
	summaries := cli.schedulerList(cli.sessionCtx, scheduler.ListFilter{})
	return scheduler.StatusLine(summaries)
}

// resolveMCPConfigPath returns the path chatcli should consult for
// MCP server configuration: the explicit `CHATCLI_MCP_CONFIG`
// override when set, otherwise the conventional `~/.chatcli/mcp_servers.json`.
// Extracted so tests can drive the auto-enable decision without
// stomping on the real user environment.
func resolveMCPConfigPath() string {
	if p := os.Getenv("CHATCLI_MCP_CONFIG"); p != "" {
		return p
	}
	return mcp.DefaultConfigPath()
}

// shouldAutoEnableMCP returns true when MCP should be initialized
// automatically (i.e., without `CHATCLI_MCP_ENABLED=true`). It says
// yes when either the config file already exists OR its parent
// directory exists — the latter is what keeps hot-reload alive when
// the user opens chatcli with no `mcp_servers.json` and creates one
// later: we still need the fsnotify watcher running on the parent
// directory so the Create event can fire Reload.
func shouldAutoEnableMCP(mcpConfigPath string) bool {
	if _, err := os.Stat(mcpConfigPath); err == nil { //#nosec G304 -- env-supplied path; only Stat, no read
		return true
	}
	if info, err := os.Stat(filepath.Dir(mcpConfigPath)); err == nil && info.IsDir() { //#nosec G304 -- env-supplied path; only Stat of parent directory
		return true
	}
	return false
}

// bootstrapMCP wires up the MCP manager + config watcher during
// chatcli startup. Pulled out of NewChatCLI so the auto-enable rule,
// the LoadConfig-tolerates-failure path, and the watcher handoff
// are testable in isolation. Safe no-op when MCP is not enabled by
// the env var and neither the config file nor its parent directory
// exists.
//
// LoadConfig errors (0-byte file, malformed JSON, …) are logged but
// don't abort initialization — we still register the manager and
// start the watcher so the user can fix the file in place and have
// it picked up on save instead of having to restart chatcli.
func (cli *ChatCLI) bootstrapMCP(ctx context.Context, logger *zap.Logger) {
	mcpEnabled := os.Getenv("CHATCLI_MCP_ENABLED") == "true"
	mcpConfigPath := resolveMCPConfigPath()
	if !mcpEnabled && shouldAutoEnableMCP(mcpConfigPath) {
		mcpEnabled = true
	}
	if !mcpEnabled {
		return
	}
	mcpMgr := mcp.NewManager(logger)
	if err := mcpMgr.LoadConfig(mcpConfigPath); err != nil {
		logger.Warn("Failed to load MCP config (will retry on file change)",
			zap.String("path", mcpConfigPath), zap.Error(err))
	}
	// The MCP manager outlives any single request; its lifecycle is governed
	// by mcpCancel (fired in cleanup). Detach request cancellation while
	// inheriting context values.
	mcpCtx, mcpCancelFn := context.WithCancel(context.WithoutCancel(ctx))
	cli.mcpManager = mcpMgr
	cli.mcpCancel = mcpCancelFn
	cli.mcpConfigPath = mcpConfigPath
	cli.mcpCtx = mcpCtx
	cli.mcpStartupDone = make(chan struct{})
	go func() {
		defer close(cli.mcpStartupDone)
		_ = mcpMgr.StartAll(mcpCtx)
		statuses := mcpMgr.GetServerStatus()
		tools := mcpMgr.GetTools()
		logger.Info("MCP manager initialized (client mode)",
			zap.Int("servers", len(statuses)),
			zap.Int("tools", len(tools)))
	}()
	cli.startMCPConfigWatcher()
	cli.initChannelTriggers()
}

// startMCPConfigWatcher boots an fsnotify watcher on the MCP config
// file's directory and triggers Manager.Reload on edits, so changes
// to mcp_servers.json take effect without restarting chatcli.
//
// We watch the directory rather than the file because most editors
// rewrite via rename (write to tmp, rename over the target), which a
// per-file watcher misses entirely. Events are debounced to avoid
// reloading mid-write.
func (cli *ChatCLI) startMCPConfigWatcher() {
	if cli.mcpManager == nil || cli.mcpConfigPath == "" {
		return
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		cli.logger.Warn("MCP config hot-reload disabled: cannot create watcher", zap.Error(err))
		return
	}
	dir := filepath.Dir(cli.mcpConfigPath)
	if err := w.Add(dir); err != nil {
		cli.logger.Warn("MCP config hot-reload disabled: cannot watch dir",
			zap.String("dir", dir), zap.Error(err))
		_ = w.Close()
		return
	}
	cli.mcpWatcher = w
	cli.mcpWatcherDone = make(chan struct{})

	go func() {
		var debounce *time.Timer
		fire := func() {
			diff, err := cli.mcpManager.Reload(cli.mcpCtx, cli.mcpConfigPath)
			if err != nil {
				cli.logger.Warn("MCP config reload failed", zap.Error(err))
				return
			}
			if len(diff.Started)+len(diff.Stopped)+len(diff.Updated) == 0 {
				return
			}
			cli.logger.Info("MCP config hot-reloaded",
				zap.Strings("started", diff.Started),
				zap.Strings("stopped", diff.Stopped),
				zap.Strings("updated", diff.Updated))
		}
		for {
			select {
			case <-cli.mcpWatcherDone:
				return
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				// Only react to events on the config file itself (the
				// directory watcher sees siblings too).
				if filepath.Clean(ev.Name) != filepath.Clean(cli.mcpConfigPath) {
					continue
				}
				if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) == 0 {
					continue
				}
				if debounce != nil {
					debounce.Stop()
				}
				debounce = time.AfterFunc(400*time.Millisecond, fire)
			case err, ok := <-w.Errors:
				if !ok {
					return
				}
				cli.logger.Debug("MCP config watcher error", zap.Error(err))
			}
		}
	}()
}

// stopMCPConfigWatcher tears down the hot-reload watcher. Safe to
// call when no watcher was ever started.
func (cli *ChatCLI) stopMCPConfigWatcher() {
	if cli.mcpWatcherDone != nil {
		select {
		case <-cli.mcpWatcherDone:
		default:
			close(cli.mcpWatcherDone)
		}
	}
	if cli.mcpWatcher != nil {
		_ = cli.mcpWatcher.Close()
		cli.mcpWatcher = nil
	}
}

func (cli *ChatCLI) cleanup(ctx context.Context) {
	// Teardown should not be aborted if the caller's context was already
	// cancelled (e.g. Ctrl+C); detach cancellation but inherit values.
	ctx = context.WithoutCancel(ctx)

	// Close the on-disk hub opened by local mode, if any.
	if cli.hubLocalClose != nil {
		cli.hubLocalClose()
		cli.hubLocalClose = nil
	}

	// Learned token ratios and today's spend outlive the process; the last
	// metrics snapshot leaves with them.
	cli.calibrator().flushCalibration()
	cli.costTracker.FlushDailySpend()
	cli.shutdownTelemetry(ctx)

	// Stop context watchers before the stores go away.
	if cli.contextHandler != nil {
		cli.contextHandler.Close()
	}

	// Release explicit cache resources so their storage stops with the
	// session (best effort, bounded).
	if n := client.ReleaseCacheResources(ctx); n > 0 && cli.logger != nil {
		cli.logger.Debug("released explicit cache resources", zap.Int("count", n))
	}

	// Flush and detach the LLM request audit trail.
	if cli.llmAudit != nil {
		cli.llmAudit.close()
		cli.llmAudit = nil
	}

	// Fire SessionEnd hook
	if cli.hookManager != nil {
		wd, _ := os.Getwd()
		cli.hookManager.Fire(ctx, hooks.HookEvent{
			Type:       hooks.EventSessionEnd,
			Timestamp:  time.Now(),
			SessionID:  cli.currentSessionName,
			WorkingDir: wd,
		})
	}

	// Auto-save the conversation before anything shuts down, so an exit
	// never costs the user a retrievable session (see cli_session_autosave.go).
	cli.autosaveSessionOnExit()

	// Record session end for usage pattern tracking
	if cli.memoryStore != nil && !cli.sessionStartTime.IsZero() {
		if mgr := cli.memoryStore.Manager(); mgr != nil {
			mgr.Patterns.RecordSessionEnd(time.Since(cli.sessionStartTime))
		}
	}

	// The unextracted tail of this session is queued for the next one
	// (the last turns of every REPL session used to be lost), then the
	// worker is stopped with a bounded wait for an in-flight pass.
	if cli.memWorker != nil {
		cli.queueMemoryBeforeCompaction()
		cli.memWorker.stopAndWait(memoryStopWait)
	}

	// Drain and shut down the durable reflexion queue. Pending jobs
	// remain in the WAL and will be replayed on the next boot.
	cli.reflexionRunnerMu.Lock()
	rnr := cli.reflexionRunner
	cli.reflexionRunnerMu.Unlock()
	if rnr != nil {
		rnr.DrainAndShutdown(30 * time.Second)
	}

	// Drain and shut down the scheduler subsystem.
	cli.shutdownScheduler()

	// Stop MCP servers (and the hot-reload watcher first so it can't
	// fire a Reload mid-shutdown). Bound the whole MCP teardown at
	// 5s so a stuck transport cannot stall the CLI's exit path.
	cli.stopMCPConfigWatcher()
	cli.shutdownChannelTriggers()
	if cli.mcpManager != nil {
		stopCtx, cancelStop := context.WithTimeout(ctx, 5*time.Second)
		cli.mcpManager.StopAll(stopCtx)
		_ = cli.mcpManager.CloseChannels()
		cancelStop()
	}
	if cli.mcpCancel != nil {
		cli.mcpCancel()
	}

	if err := cli.historyManager.AppendAndRotateHistory(cli.newCommandsInSession); err != nil {
		cli.logger.Error("Erro ao salvar histórico", zap.Error(err))
	}
	if cli.Client != nil {
		if assistantClient, ok := cli.Client.(*openaiassistant.OpenAIAssistantClient); ok {
			if err := assistantClient.Cleanup(ctx); err != nil {
				cli.logger.Error("Erro na limpeza do OpenAI Assistant", zap.Error(err))
			}
		}
	}
	if cli.pluginManager != nil {
		cli.pluginManager.Close()
	}
	// Shut down any language servers the @lsp tool started.
	cli.shutdownLSPPool()
	// Stop any background processes the @proc tool started.
	cli.shutdownProcSupervisor()
	// Close the browser session the @browser tool may have launched — a
	// headless Chrome must never outlive ChatCLI.
	browser.Shutdown(ctx)
	// Cancel an in-flight task graph run so its workers stop with the
	// session, and stop the dashboard server if one was opened.
	if cli.taskGraphAdapter != nil {
		if _, err := cli.taskGraphAdapter.Cancel(); err == nil {
			cli.logger.Info("task graph run canceled on shutdown")
		}
		cli.taskGraphAdapter.shutdownDash(ctx)
	}

	// Tear down the session scratch workspace. Respects
	// CHATCLI_AGENT_KEEP_TMPDIR=true for debugging (files are left behind).
	if ws := agent.GetSessionWorkspace(); ws != nil {
		ws.Cleanup()
	}
	// Final cost snapshot so /cost last in the next session sees this one
	// complete (the write-through during the session is throttled).
	if cli.costTracker != nil {
		cli.costTracker.SetSessionName(cli.currentSessionName)
		if err := cli.costTracker.SaveSession(); err != nil {
			cli.logger.Debug("cost snapshot save on shutdown failed", zap.Error(err))
		}
	}
	if err := cli.logger.Sync(); err != nil {
		msg := err.Error()
		if !strings.Contains(msg, "/dev/stdout") &&
			!strings.Contains(msg, "/dev/stderr") &&
			!strings.Contains(msg, "invalid argument") &&
			!strings.Contains(msg, "inappropriate ioctl") {
			fmt.Fprintf(os.Stderr, "Falha ao sincronizar logger: %v\n", err)
		}
	}
}
