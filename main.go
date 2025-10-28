/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/diillson/chatcli/cli"
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/manager"
	"github.com/diillson/chatcli/utils"
	"github.com/diillson/chatcli/version"
	"github.com/joho/godotenv"
	"go.uber.org/zap"
)

func main() {
	args := cli.PreprocessArgs(os.Args[1:])
	opts, err := cli.Parse(args)
	if err != nil {
		fmt.Println(err)
		os.Exit(2)
	}

	i18n.Init()

	if opts.Version {
		versionInfo := version.GetCurrentVersion()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		latest, hasUpdate, err := version.CheckLatestVersionWithContext(ctx)
		fmt.Println(version.FormatVersionInfo(versionInfo, latest, hasUpdate, err))
		return
	}

	envFilePath := os.Getenv("CHATCLI_DOTENV")
	if envFilePath == "" {
		envFilePath = ".env"
	} else {
		expanded, err := utils.ExpandPath(envFilePath)
		if err == nil {
			envFilePath = expanded
		} else {
			fmt.Println(i18n.T("main.warn_expand_path", envFilePath, err))
		}
	}

	if err := godotenv.Load(envFilePath); err != nil && !os.IsNotExist(err) {
		// CORREÇÃO: Usar Println com i18n.T
		fmt.Println(i18n.T("main.error_dotenv_not_found", envFilePath))
	}

	logger, err := utils.InitializeLogger()
	if err != nil {
		// CORREÇÃO: Usar Println com i18n.T
		fmt.Println(i18n.T("main.error_logger_init", err))
		os.Exit(1)
	}

	config.Global = config.New(logger)
	config.Global.Load()
	utils.LogStartupInfo(logger)

	defer func() {
		if err := logger.Sync(); err != nil {
			fmt.Printf("Erro ao fechar logger: %v\n", err)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	llmManager, err := manager.NewLLMManager(logger)
	if err != nil {
		logger.Fatal("Erro ao inicializar o LLMManager", zap.Error(err))
	}

	availableProviders := llmManager.GetAvailableProviders()
	if len(availableProviders) == 0 && (opts.PromptFlagUsed || cli.HasStdin()) {
		logger.Warn("Nenhum provedor LLM configurado via .env, dependendo de flags para funcionar.")
	} else if len(availableProviders) == 0 {
		fmt.Println(i18n.T("main.error_no_provider"))
		os.Exit(1)
	}

	chatCLI, err := cli.NewChatCLI(llmManager, logger)
	if err != nil {
		logger.Fatal("Erro ao inicializar o ChatCLI", zap.Error(err))
	}

	chatCLI.UserMaxTokens = opts.MaxTokens

	targetProvider := opts.Provider
	if targetProvider == "" {
		targetProvider = chatCLI.Provider
	}

	if strings.ToUpper(targetProvider) == "STACKSPOT" {
		if opts.Realm != "" {
			llmManager.SetStackSpotRealm(opts.Realm)
			logger.Info("Realm/Tenant do StackSpot sobrescrito via flag", zap.String("realm", opts.Realm))
		}
		if opts.AgentID != "" {
			llmManager.SetStackSpotAgentID(opts.AgentID)
			logger.Info("Agent ID do StackSpot sobrescrito via flag", zap.String("agent-id", opts.AgentID))
		}
	}

	if err := chatCLI.ApplyOverrides(llmManager, opts.Provider, opts.Model); err != nil {
		// CORREÇÃO: Usar Fprintln com i18n.T
		fmt.Fprintln(os.Stderr, i18n.T("main.error_apply_overrides", err))
		logger.Error("Erro fatal ao aplicar overrides de provider/model via flags", zap.Error(err))
		os.Exit(1)
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT)

	go func() {
		for range sigChan {
			if chatCLI.IsExecuting() {
				logger.Info("Cancelando operação em andamento")
				chatCLI.CancelOperation()
			} else {
				logger.Info("Encerrando aplicação")
				os.Exit(0)
			}
		}
	}()

	if chatCLI.HandleOneShotOrFatal(ctx, opts) {
		return
	}

	handleGracefulShutdown(cancel, logger)

	chatCLI.Start(ctx)
}

func handleGracefulShutdown(cancelFunc context.CancelFunc, logger *zap.Logger) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-signals
		logger.Info("Recebido sinal para finalizar a aplicação", zap.String("sinal", sig.String()))
		cancelFunc()
	}()
}
