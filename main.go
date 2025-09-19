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
	"syscall"
	"time"

	"github.com/diillson/chatcli/cli"
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/llm/manager"
	"github.com/diillson/chatcli/utils"
	"github.com/diillson/chatcli/version"
	"github.com/joho/godotenv"
	"go.uber.org/zap"
)

func main() {

	// Parse das flags
	args := cli.PreprocessArgs(os.Args[1:])
	opts, err := cli.Parse(args)
	if err != nil {
		fmt.Println(err)
		os.Exit(2)
	}

	// Saída antecipada para --version
	if opts.Version {
		versionInfo := version.GetCurrentVersion()

		// Checagem com timeout
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		latest, hasUpdate, err := version.CheckLatestVersionWithContext(ctx)

		fmt.Println(version.FormatVersionInfo(versionInfo, latest, hasUpdate, err))
		return
	}

	// Mensagem de versão no startup
	//version.PrintStartupVersionInfo()

	// Carregar variáveis de ambiente do arquivo .env
	envFilePath := os.Getenv("CHATCLI_DOTENV")
	if envFilePath == "" {
		envFilePath = ".env"
	} else {
		expanded, err := utils.ExpandPath(envFilePath)
		if err == nil {
			envFilePath = expanded
		} else {
			fmt.Printf("Aviso: não foi possível expandir o caminho '%s': %v\n", envFilePath, err)
		}
	}

	if err := godotenv.Load(envFilePath); err != nil && !os.IsNotExist(err) {
		fmt.Printf("Não foi encontrado o arquivo .env em %s\n", envFilePath)
	}

	// Inicializar o logger
	logger, err := utils.InitializeLogger()
	if err != nil {
		fmt.Printf("Não foi possível inicializar o logger: %v\n", err)
		os.Exit(1) // Encerrar a aplicação em caso de erro crítico
	}

	utils.LogStartupInfo(logger)

	defer func() {
		if err := logger.Sync(); err != nil {
			fmt.Printf("Erro ao fechar logger: %v\n", err)
		}
	}()

	// Configurar o contexto para o shutdown gracioso
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Verificar variáveis de ambiente e informar o usuário
	utils.CheckEnvVariables(logger)
	// Inicializar o LLMManager com as constantes do pacote config
	slugName := utils.GetEnvOrDefault("SLUG_NAME", config.DefaultSlugName)
	tenantName := utils.GetEnvOrDefault("TENANT_NAME", config.DefaultTenantName)
	llmManager, err := manager.NewLLMManager(logger, slugName, tenantName)
	if err != nil {
		logger.Fatal("Erro ao inicializar o LLMManager", zap.Error(err))
	}

	// Verificar se há provedores disponíveis
	availableProviders := llmManager.GetAvailableProviders()
	if len(availableProviders) == 0 {
		fmt.Println("Nenhum provedor LLM está configurado. Verifique suas variáveis de ambiente.")
		os.Exit(1)
	}

	// Inicializar e iniciar o ChatCLI
	chatCLI, err := cli.NewChatCLI(llmManager, logger)
	if err != nil {
		logger.Fatal("Erro ao inicializar o ChatCLI", zap.Error(err))
	}

	// Aplicar overrides de provider, model e max-tokens do modo one-shot, se fornecidos
	chatCLI.UserMaxTokens = opts.MaxTokens
	if err := chatCLI.ApplyOverrides(llmManager, opts.Provider, opts.Model); err != nil {
		// Imprimir no stderr para o usuário e para os testes E2E
		fmt.Fprintf(os.Stderr, " ❌ Erro ao aplicar overrides: %v\n", err)
		logger.Error("Erro fatal ao aplicar overrides de provider/model via flags", zap.Error(err))
		os.Exit(1)
	}

	// Configurar manipulador de sinais DEPOIS de criar chatCLI
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT)

	go func() {
		for range sigChan {
			// Este é o truque: verificamos se está executando
			if chatCLI.IsExecuting() {
				logger.Info("Cancelando operação em andamento")
				chatCLI.CancelOperation()
				// NÃO sair do programa, apenas cancelar a operação
			} else {
				logger.Info("Encerrando aplicação")
				os.Exit(0)
			}
		}
	}()

	// Modo one-shot: se acionado, tratar e sair
	if chatCLI.HandleOneShotOrFatal(ctx, opts) {
		return
	}

	handleGracefulShutdown(cancel, logger)

	// Caso não for oneshot, segue no modo interativo
	chatCLI.Start(ctx)
}

func handleGracefulShutdown(cancelFunc context.CancelFunc, logger *zap.Logger) {
	signals := make(chan os.Signal, 1)
	// Capturar sinais de interrupção e término
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-signals
		logger.Info("Recebido sinal para finalizar a aplicação", zap.String("sinal", sig.String()))
		cancelFunc()
	}()
}
