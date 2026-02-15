/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package cmd

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/cli"
	"github.com/diillson/chatcli/client/remote"
	"github.com/diillson/chatcli/llm/manager"
	"go.uber.org/zap"
)

// ConnectOptions holds the flags for the 'connect' subcommand.
type ConnectOptions struct {
	Address      string
	Token        string
	TLS          bool
	CertFile     string
	ClientAPIKey string // client's own LLM API key/OAuth token (forwarded to server)
	UseLocalAuth bool   // resolve LLM credentials from local auth store (~/.chatcli/auth-profiles.json)
	Provider     string // override server's default LLM provider
	Model        string // override server's default LLM model

	// StackSpot-specific
	ClientID  string // StackSpot client ID
	ClientKey string // StackSpot client key
	Realm     string // StackSpot realm/tenant
	AgentID   string // StackSpot agent ID

	// Ollama-specific
	OllamaURL string // Ollama base URL

	// one-shot mode via connect
	Prompt    string
	Raw       bool
	MaxTokens int
}

// llmProviderToAuthProvider maps LLMManager provider names to auth.ProviderID.
func llmProviderToAuthProvider(provider string) (auth.ProviderID, bool) {
	switch strings.ToUpper(provider) {
	case "CLAUDEAI":
		return auth.ProviderAnthropic, true
	case "OPENAI":
		return auth.ProviderOpenAI, true
	default:
		return "", false
	}
}

// RunConnect executes the 'chatcli connect' subcommand.
func RunConnect(ctx context.Context, args []string, llmMgr manager.LLMManager, logger *zap.Logger) error {
	// Pre-process: if the first arg is not a flag, extract it as the positional address.
	// Go's flag.Parse stops at the first non-flag argument, so "connect addr --token X"
	// would leave --token unparsed. By extracting it first, all flags parse correctly.
	var positionalAddr string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		positionalAddr = args[0]
		args = args[1:]
	}

	fs := flag.NewFlagSet("connect", flag.ContinueOnError)

	opts := &ConnectOptions{}
	fs.StringVar(&opts.Address, "addr", os.Getenv("CHATCLI_REMOTE_ADDR"), "Remote server address (host:port)")
	fs.StringVar(&opts.Token, "token", os.Getenv("CHATCLI_REMOTE_TOKEN"), "Authentication token")
	fs.BoolVar(&opts.TLS, "tls", false, "Enable TLS")
	fs.StringVar(&opts.CertFile, "ca-cert", "", "CA certificate file for TLS")
	fs.StringVar(&opts.ClientAPIKey, "llm-key", os.Getenv("CHATCLI_CLIENT_API_KEY"), "Your own LLM API key/OAuth token (forwarded to server)")
	fs.BoolVar(&opts.UseLocalAuth, "use-local-auth", false, "Use OAuth/API key from local auth store (~/.chatcli/auth-profiles.json)")
	fs.StringVar(&opts.Provider, "provider", "", "Override server's default LLM provider (e.g., OPENAI, CLAUDEAI, GOOGLEAI, XAI, STACKSPOT, OLLAMA)")
	fs.StringVar(&opts.Model, "model", "", "Override server's default LLM model (e.g., gpt-4, gemini-2.0-flash)")
	fs.StringVar(&opts.ClientID, "client-id", "", "StackSpot: Client ID for authentication")
	fs.StringVar(&opts.ClientKey, "client-key", "", "StackSpot: Client Key for authentication")
	fs.StringVar(&opts.Realm, "realm", "", "StackSpot: Realm/Tenant")
	fs.StringVar(&opts.AgentID, "agent-id", "", "StackSpot: Agent ID")
	fs.StringVar(&opts.OllamaURL, "ollama-url", "", "Ollama: Base URL (e.g., http://localhost:11434)")
	fs.StringVar(&opts.Prompt, "p", "", "One-shot prompt (sends and exits)")
	fs.BoolVar(&opts.Raw, "raw", false, "Raw output (no formatting)")
	fs.IntVar(&opts.MaxTokens, "max-tokens", 0, "Max tokens for response")

	if err := fs.Parse(args); err != nil {
		return err
	}

	// Accept address as positional argument: chatcli connect localhost:50051
	if opts.Address == "" && positionalAddr != "" {
		opts.Address = positionalAddr
	} else if opts.Address == "" && fs.NArg() > 0 {
		opts.Address = fs.Arg(0)
	}

	if opts.Address == "" {
		PrintConnectUsage()
		return fmt.Errorf("server address is required (use --addr or positional argument)")
	}

	// Resolve local auth if requested
	if opts.UseLocalAuth && opts.ClientAPIKey == "" {
		resolvedKey, resolvedProvider, err := resolveLocalAuth(ctx, opts.Provider, logger)
		if err != nil {
			return fmt.Errorf("failed to resolve local auth: %w", err)
		}
		opts.ClientAPIKey = resolvedKey
		// If provider wasn't explicitly set, use the one from auth resolution
		if opts.Provider == "" {
			opts.Provider = resolvedProvider
		}
	}

	// Build provider-specific config
	providerConfig := make(map[string]string)
	if opts.ClientID != "" {
		providerConfig["client_id"] = opts.ClientID
	}
	if opts.ClientKey != "" {
		providerConfig["client_key"] = opts.ClientKey
	}
	if opts.Realm != "" {
		providerConfig["realm"] = opts.Realm
	}
	if opts.AgentID != "" {
		providerConfig["agent_id"] = opts.AgentID
	}
	if opts.OllamaURL != "" {
		providerConfig["base_url"] = opts.OllamaURL
	}

	// Create remote client
	remoteCfg := remote.Config{
		Address:        opts.Address,
		Token:          opts.Token,
		TLS:            opts.TLS,
		CertFile:       opts.CertFile,
		ClientAPIKey:   opts.ClientAPIKey,
		Provider:       opts.Provider,
		Model:          opts.Model,
		ProviderConfig: providerConfig,
	}

	remoteClient, err := remote.NewClient(remoteCfg, logger)
	if err != nil {
		return fmt.Errorf("failed to connect to remote server: %w", err)
	}
	defer remoteClient.Close()

	// Health check
	healthy, ver, err := remoteClient.Health(ctx)
	if err != nil {
		return fmt.Errorf("server health check failed: %w", err)
	}
	if !healthy {
		return fmt.Errorf("server is not healthy")
	}

	connInfo := fmt.Sprintf("version: %s, provider: %s, model: %s", ver, remoteClient.GetProvider(), remoteClient.GetModelName())
	if opts.UseLocalAuth {
		connInfo += ", using local OAuth credentials"
	} else if opts.ClientAPIKey != "" {
		connInfo += ", using your own API key"
	}
	fmt.Printf("Connected to ChatCLI server (%s)\n", connInfo)

	// Check if server has K8s watcher active
	if info, err := remoteClient.GetServerInfo(ctx); err == nil && info.WatcherActive {
		fmt.Printf("K8s watcher active: %s (context injected into all prompts)\n", info.WatcherTarget)
	}

	// One-shot mode via connect
	if opts.Prompt != "" {
		response, err := remoteClient.SendPrompt(ctx, opts.Prompt, nil, opts.MaxTokens)
		if err != nil {
			return fmt.Errorf("remote prompt failed: %w", err)
		}
		fmt.Println(response)
		return nil
	}

	// Interactive mode: create ChatCLI with remote client as the LLM backend
	chatCLI, err := cli.NewChatCLI(llmMgr, logger)
	if err != nil {
		return fmt.Errorf("failed to initialize ChatCLI: %w", err)
	}

	// Override the LLM client with the remote client
	chatCLI.Client = remoteClient
	chatCLI.Provider = remoteClient.GetProvider()
	chatCLI.Model = remoteClient.GetModelName()

	chatCLI.Start(ctx)
	return nil
}

// resolveLocalAuth reads the local auth store and returns the API key/OAuth token
// for the given provider. If provider is empty, it tries Anthropic first, then OpenAI.
func resolveLocalAuth(ctx context.Context, provider string, logger *zap.Logger) (apiKey string, resolvedProvider string, err error) {
	// If provider specified, resolve directly
	if provider != "" {
		authProvider, ok := llmProviderToAuthProvider(provider)
		if !ok {
			return "", "", fmt.Errorf(
				"--use-local-auth only supports OAuth providers (CLAUDEAI, OPENAI). "+
					"Provider '%s' requires --llm-key with an API key instead", provider)
		}

		resolved, err := auth.ResolveAuth(ctx, authProvider, logger)
		if err != nil {
			return "", "", fmt.Errorf("no local credentials found for %s: %w\n"+
				"Run 'chatcli' then '/auth login %s' first", provider, err, string(authProvider))
		}
		return resolved.APIKey, provider, nil
	}

	// No provider specified: try each OAuth provider in order
	for _, candidate := range []struct {
		authProvider auth.ProviderID
		llmProvider  string
	}{
		{auth.ProviderAnthropic, "CLAUDEAI"},
		{auth.ProviderOpenAI, "OPENAI"},
	} {
		resolved, err := auth.ResolveAuth(ctx, candidate.authProvider, logger)
		if err == nil && resolved.APIKey != "" {
			logger.Info("Auto-resolved local auth",
				zap.String("provider", candidate.llmProvider),
				zap.String("source", resolved.Source),
				zap.String("mode", string(resolved.Mode)),
			)
			return resolved.APIKey, candidate.llmProvider, nil
		}
	}

	return "", "", fmt.Errorf("no local OAuth credentials found. Run 'chatcli' then '/auth login anthropic' or '/auth login openai-codex' first")
}

// PrintConnectUsage prints help for the connect subcommand.
func PrintConnectUsage() {
	fmt.Println(`Usage: chatcli connect [flags] [address]

Connect to a remote ChatCLI gRPC server.

Arguments:
  address               Server address (host:port)

Flags:
  --addr <host:port>    Server address (env: CHATCLI_REMOTE_ADDR)
  --token <string>      Server auth token (env: CHATCLI_REMOTE_TOKEN)
  --provider <string>   Override LLM provider (OPENAI, CLAUDEAI, GOOGLEAI, XAI, STACKSPOT, OLLAMA)
  --model <string>      Override LLM model (e.g., gpt-4, gemini-2.0-flash)
  --llm-key <string>    Your own LLM API key/OAuth token (env: CHATCLI_CLIENT_API_KEY)
  --use-local-auth      Use OAuth credentials from local auth store (from /auth login)
  --tls                 Enable TLS connection
  --ca-cert <path>      CA certificate file for TLS verification
  -p <prompt>           One-shot mode: send prompt and exit
  --raw                 Raw output (no markdown/ANSI formatting)
  --max-tokens <int>    Max tokens for response

  StackSpot flags:
  --client-id <string>  StackSpot Client ID
  --client-key <string> StackSpot Client Key
  --realm <string>      StackSpot Realm/Tenant
  --agent-id <string>   StackSpot Agent ID

  Ollama flags:
  --ollama-url <url>    Ollama server base URL (e.g., http://localhost:11434)

Credential modes (pick one):
  1. Server credentials (default): Server uses its own API keys from env vars
  2. --use-local-auth: Reads your local OAuth token (from /auth login) and forwards it
  3. --llm-key <key>: Manually pass an API key or OAuth token (OPENAI, CLAUDEAI, GOOGLEAI, XAI)
  4. --client-id + --client-key + --realm + --agent-id: StackSpot credentials
  5. --ollama-url: Connect to an Ollama server (no credentials needed)

Examples:
  # Use server's default credentials
  chatcli connect localhost:50051

  # Use your local Anthropic OAuth token (from /auth login anthropic)
  chatcli connect myserver:50051 --use-local-auth

  # Use local auth with a specific provider
  chatcli connect myserver:50051 --use-local-auth --provider CLAUDEAI

  # Manually pass an API key (OpenAI, Claude, Google, xAI)
  chatcli connect myserver:50051 --provider GOOGLEAI --llm-key AIzaSy-xxx
  chatcli connect myserver:50051 --provider XAI --llm-key xai-xxx

  # StackSpot with full credentials
  chatcli connect myserver:50051 --provider STACKSPOT \
    --client-id <id> --client-key <key> --realm <realm> --agent-id <agent>

  # Ollama (running on the server or accessible URL)
  chatcli connect myserver:50051 --provider OLLAMA --ollama-url http://gpu-server:11434

  # One-shot with local auth
  chatcli connect myserver:50051 --use-local-auth -p "Explain K8s pods"`)
}
