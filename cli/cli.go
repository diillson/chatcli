/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
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
	"github.com/diillson/chatcli/cli/paste"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/cli/workspace"
	"github.com/diillson/chatcli/client/remote"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/manager"
	"github.com/diillson/chatcli/llm/openai_assistant"

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

type InteractionState int

type ExecutionProfile int

const (
	StateNormal InteractionState = iota
	StateSwitchingProvider
	StateProcessing
	StateAgentMode
	ProfileNormal ExecutionProfile = iota
	ProfileAgent
	ProfileCoder
)

var agentModeRequest = errors.New("request to enter agent mode")
var coderModeRequest = errors.New("request to enter coder mode")
var errExitRequest = errors.New("request to exit")
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
	"/switch": {
		"--model":      {},
		"--max-tokens": {},
		"--realm":      {},
		"--agent-id":   {},
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
	commandHistory       []string
	newCommandsInSession []string
	historyManager       *HistoryManager
	animation            *AnimationManager
	commandHandler       *CommandHandler
	lastCommandOutput    string
	fileChunks           []FileChunk // Chunks pendentes para processamento
	failedChunks         []FileChunk // Chunks que falharam no processamento
	lastFailedChunk      *FileChunk  // Referência ao último chunk que falhou
	agentMode            *AgentMode  // Modo de agente
	interactionState     InteractionState
	mu                   sync.Mutex
	operationCancel      context.CancelFunc
	isExecuting          atomic.Bool
	processingDone       chan struct{}
	messageQueue         []string   // FIFO queue of user messages typed during processing
	messageQueueMu       sync.Mutex // protects messageQueue
	prefixSpinnerIdx     int32      // atomic counter for animated prefix spinner
	sessionManager       *SessionManager
	currentSessionName   string
	UserMaxTokens        int
	pluginManager        *plugins.Manager
	contextHandler       *ContextHandler
	personaHandler       *PersonaHandler
	skillHandler         *SkillHandler
	executionProfile     ExecutionProfile
	pendingAction        string // stores intended action before panic (for Windows go-prompt tearDown workaround)

	// Remote connection state (for /connect and /disconnect)
	localClient   client.LLMClient           // saved local client when connected to remote
	localProvider string                     // saved local provider name
	localModel    string                     // saved local model name
	remoteConn    interface{ Close() error } // remote connection (for cleanup on disconnect)
	isRemote      bool                       // true when connected to a remote server

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

	// Conversation checkpoints for rewind
	checkpoints []conversationCheckpoint
	lastEscTime time.Time // for Esc+Esc double-press detection

	// Background memory annotation worker
	memWorker        *memoryWorker
	sessionStartTime time.Time // for session duration tracking
}

// NewChatCLI cria uma nova instância de ChatCLI
func NewChatCLI(manager manager.LLMManager, logger *zap.Logger) (*ChatCLI, error) {
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

	pluginMgr, err := plugins.NewManager(logger)
	if err != nil {
		// Logamos o erro, mas a aplicação continua. O pluginManager será um objeto válido, mas vazio.
		logger.Error("Falha crítica ao inicializar o gerenciador de plugins, plugins estarão desabilitados", zap.Error(err))
	}
	cli.pluginManager = pluginMgr
	if pluginMgr != nil {
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinCoderPlugin())
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

	// Initialize workspace context (bootstrap files + memory)
	homeDir, _ := os.UserHomeDir()
	globalDir := filepath.Join(homeDir, ".chatcli")
	workspaceDir, _ := os.Getwd()

	// CHATCLI_BOOTSTRAP_DIR overrides the global bootstrap directory
	if envDir := os.Getenv("CHATCLI_BOOTSTRAP_DIR"); envDir != "" {
		globalDir = envDir
	}

	bootstrapEnabled := os.Getenv("CHATCLI_BOOTSTRAP_ENABLED") != "false"
	memoryEnabled := os.Getenv("CHATCLI_MEMORY_ENABLED") != "false"

	var bootstrapLoader *workspace.BootstrapLoader
	if bootstrapEnabled {
		bootstrapLoader = workspace.NewBootstrapLoader(workspaceDir, globalDir, logger)
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
	cli.contextBuilder = workspace.NewContextBuilder(bootstrapLoader, memStore)

	// Start background memory annotation worker
	if memoryEnabled {
		cli.memWorker = newMemoryWorker(cli)
		cli.memWorker.start()
		// Record session start for usage pattern tracking
		cli.sessionStartTime = time.Now()
		if mgr := memStore.Manager(); mgr != nil {
			mgr.Patterns.RecordSessionStart()
		}
	}

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
	cli.agentMode = NewAgentMode(cli, logger)

	history, err := cli.historyManager.LoadHistory()
	if err != nil {
		cli.logger.Error("Erro ao carregar o histórico", zap.Error(err))
	} else {
		cli.commandHistory = history
	}

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

	if in != "" {
		cli.commandHistory = append(cli.commandHistory, in)
		cli.newCommandsInSession = append(cli.newCommandsInSession, in)
	}

	if strings.HasPrefix(in, "/run") {
		cli.pendingAction = "agent"
		panic(agentModeRequest)
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

	if strings.HasPrefix(in, "/") || in == "exit" || in == "quit" {
		if cli.commandHandler.HandleCommand(in) {
			cli.pendingAction = "exit"
			panic(errExitRequest)
		}
		return
	}

	if cli.Client == nil {
		fmt.Println(i18n.T("cli.error.no_provider_configured"))
		return
	}

	// If already processing, queue the message for later (type-ahead)
	if cli.isExecuting.Load() {
		cli.messageQueueMu.Lock()
		if len(cli.messageQueue) < 10 {
			cli.messageQueue = append(cli.messageQueue, in)
			queueLen := len(cli.messageQueue)
			cli.messageQueueMu.Unlock()
			fmt.Printf("\n  %s\n", colorize(i18n.T("queue.message_queued", queueLen), ColorGray))
		} else {
			cli.messageQueueMu.Unlock()
			fmt.Printf("\n  %s\n", colorize(i18n.T("queue.full"), ColorYellow))
		}
		return
	}

	cli.interactionState = StateProcessing
	if runtime.GOOS == "windows" {
		// On Windows, run synchronously so go-prompt naturally redraws the prompt
		// after the response completes. SIGWINCH doesn't exist on Windows, so the
		// async approach leaves go-prompt waiting for a keypress before redrawing.
		// Ctrl+C still works via the OS signal handler (SIGINT) in a separate goroutine.
		cli.processLLMRequest(in)
	} else {
		go cli.processLLMRequest(in)
	}
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
	defer cli.cleanup()
	cli.PrintWelcomeScreen()

	shouldContinue := true
	for shouldContinue {
		func() {
			defer func() {
				if r := recover(); r != nil {
					// On Windows, go-prompt tearDown may panic with "close of closed channel"
					// which replaces our original panic value. Use pendingAction as fallback.
					action := cli.pendingAction
					cli.pendingAction = ""
					if r == agentModeRequest || action == "agent" {
					} else if r == coderModeRequest || action == "coder" {
						cli.restoreTerminal()
					} else if r == errExitRequest || action == "exit" {
						shouldContinue = false
					} else {
						panic(r)
					}
				}
			}()

			pasteParser := paste.NewBracketedPasteParser(
				prompt.NewStandardInputParser(),
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

			if strings.HasPrefix(lastCmd, "/coder") {
				cli.runCoderLogic()
			} else if strings.HasPrefix(lastCmd, "/run") || strings.HasPrefix(lastCmd, "/agent") {
				cli.runAgentLogic()
			}
		}
	}
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
func (cli *ChatCLI) runWithCancellation(taskName string, fn func(context.Context) error) {
	// Cria contexto cancelável
	ctx, cancel := context.WithCancel(context.Background())

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
			fmt.Println(colorize("\n "+i18n.T("cli.status.operation_cancelled_returning"), ColorYellow))
		} else {
			fmt.Println(colorize("\n "+i18n.T("cli.error.execution_failed", err), ColorRed))
		}
	}
}

func (cli *ChatCLI) runAgentLogic() {
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
	} else {
		fmt.Println(i18n.T("error.agent_query_extraction"))
		return
	}

	fmt.Println(i18n.T("status.agent_mode_enter", query))
	fmt.Println(i18n.T("status.agent_mode_description"))

	query, additionalContext := cli.processSpecialCommands(query)

	if cli.agentMode == nil {
		cli.agentMode = NewAgentMode(cli, cli.logger)
	}

	cli.runWithCancellation("Agent Mode", func(ctx context.Context) error {
		return cli.agentMode.Run(ctx, query, additionalContext, "")
	})

	// Nudge memory worker after agent run
	if cli.memWorker != nil {
		cli.memWorker.nudge()
	}

	fmt.Println(i18n.T("status.agent_mode_exit"))
}

func (cli *ChatCLI) runCoderLogic() {
	cli.setExecutionProfile(ProfileCoder)
	defer cli.setExecutionProfile(ProfileNormal)

	if len(cli.commandHistory) == 0 {
		return
	}
	lastCommand := cli.commandHistory[len(cli.commandHistory)-1]

	query := strings.TrimSpace(strings.TrimPrefix(lastCommand, "/coder"))
	if query == "" {
		fmt.Println(i18n.T("error.agent_query_extraction"))
		return
	}

	fmt.Println(colorize("\n"+i18n.T("coder.header"), ColorCyan+ColorBold))
	fmt.Printf("%s: \"%s\"\n\n", i18n.T("coder.objective"), query)
	fmt.Println(colorize(i18n.T("coder.hint.ctrl_c"), ColorGray))

	query, additionalContext := cli.processSpecialCommands(query)

	if cli.agentMode == nil {
		cli.agentMode = NewAgentMode(cli, cli.logger)
	}

	cli.runWithCancellation("Coder Mode", func(ctx context.Context) error {
		return cli.agentMode.Run(ctx, query, additionalContext, CoderSystemPrompt)
	})

	// Nudge memory worker after coder run
	if cli.memWorker != nil {
		cli.memWorker.nudge()
	}

	fmt.Println(colorize("\n "+i18n.T("coder.session_finished"), ColorGreen))
}

func (cli *ChatCLI) handleCtrlC(buf *prompt.Buffer) {
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
		cli.cleanup()
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
	switch cli.interactionState {
	case StateSwitchingProvider:
		return i18n.T("prompt.select_provider"), true
	case StateProcessing:
		spinner := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		idx := atomic.LoadInt32(&cli.prefixSpinnerIdx)
		s := spinner[int(idx)%len(spinner)]
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
		prefix := "❯ "
		if cli.currentSessionName != "" {
			prefix = fmt.Sprintf("%s ❯ ", cli.currentSessionName)
		}
		if cli.isRemote {
			prefix = "[remote] " + prefix
		}
		if cli.isWatching {
			prefix = "[watch] " + prefix
		}
		return prefix, true
	}
}

func (cli *ChatCLI) cleanup() {
	// Record session end for usage pattern tracking
	if cli.memoryStore != nil && !cli.sessionStartTime.IsZero() {
		if mgr := cli.memoryStore.Manager(); mgr != nil {
			mgr.Patterns.RecordSessionEnd(time.Since(cli.sessionStartTime))
		}
	}

	// Stop background memory worker
	if cli.memWorker != nil {
		cli.memWorker.stop()
	}

	if err := cli.historyManager.AppendAndRotateHistory(cli.newCommandsInSession); err != nil {
		cli.logger.Error("Erro ao salvar histórico", zap.Error(err))
	}
	if cli.Client != nil {
		if assistantClient, ok := cli.Client.(*openai_assistant.OpenAIAssistantClient); ok {
			if err := assistantClient.Cleanup(); err != nil {
				cli.logger.Error("Erro na limpeza do OpenAI Assistant", zap.Error(err))
			}
		}
	}
	if cli.pluginManager != nil {
		cli.pluginManager.Close()
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
