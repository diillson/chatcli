package cli

import (
	"context"
	"fmt"
	"github.com/peterh/liner"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// AgentMode representa a funcionalidade de agente autônomo no ChatCLI
type AgentMode struct {
	cli    *ChatCLI
	logger *zap.Logger
}

// NewAgentMode cria uma nova instância do modo agente
func NewAgentMode(cli *ChatCLI, logger *zap.Logger) *AgentMode {
	return &AgentMode{
		cli:    cli,
		logger: logger,
	}
}

// CommandBlock representa um bloco de comandos executáveis
type CommandBlock struct {
	Description string   // Descrição do que o comando faz
	Commands    []string // Os comandos a serem executados
	Language    string   // Linguagem (bash, python, etc.)
}

// Run inicia o modo agente com uma pergunta do usuário
func (a *AgentMode) Run(ctx context.Context, query string, additionalContext string) error {
	// 1. Enviar a pergunta para a LLM com instruções específicas sobre formato de resposta
	systemInstruction := `Você é um assistente de linha de comando que ajuda o usuário a executar tarefas no sistema.
        Quando o usuário pede para realizar uma tarefa, analise o problema e sugira o melhor comando que possam resolver a questão.
        
        Para cada bloco de código executável, use o formato:
        ` + "```" + `execute:<tipo>
        <comandos>
        ` + "```" + `
        
        Exemplos de formatação:
        
        Para comandos shell:
        ` + "```" + `execute:shell
        ls -la | grep "\.txt$"
        cat file.txt | grep "exemplo"
        ` + "```" + `
        
        Para comandos Kubernetes:
        ` + "```" + `execute:kubernetes
        kubectl get pods -n my-namespace
        kubectl describe pod my-pod -n my-namespace
        ` + "```" + `
        
        Para outros tipos de comandos, use o identificador apropriado, como:
        - execute:docker
        - execute:terraform
        - execute:git
        - execute:sql
        
        IMPORTANTE:
        1. Comece sua resposta com uma breve explicação do que você vai fazer
        2. Descreva brevemente o que cada conjunto de comandos faz antes de mostrar o bloco execute
        3. Use comando claro, simples e seguro
        4. Caso possua mais de um comando, agrupe comandos relacionados no mesmo bloco execute quando tiverem o mesmo propósito
        5. Cada comando em um bloco será executado individualmente, um após o outro
        6. Não use comandos destrutivos (rm -rf, drop database, etc.) sem avisos claros
        7. Se a tarefa for complexa, divida em múltiplos blocos execute com propósitos distintos
        
        Adapte o comando para o contexto do usuário, considerando o ambiente e as ferramentas disponíveis.`

	// 2. Adicionar a mensagem do sistema ao histórico
	a.cli.history = append(a.cli.history, models.Message{
		Role:    "system",
		Content: systemInstruction,
	})

	// 3. Adicionar a pergunta do usuário ao histórico
	fullQuery := query
	if additionalContext != "" {
		fullQuery = query + "\n\nContexto adicional:\n" + additionalContext
	}

	a.cli.history = append(a.cli.history, models.Message{
		Role:    "user",
		Content: fullQuery,
	})

	// 4. Mostrar animação "pensando..."
	a.cli.animation.ShowThinkingAnimation(a.cli.client.GetModelName())

	// 5. Enviar para a LLM e obter a resposta
	aiResponse, err := a.cli.client.SendPrompt(ctx, fullQuery, a.cli.history)
	a.cli.animation.StopThinkingAnimation()

	if err != nil {
		a.logger.Error("Erro ao obter resposta do LLM no modo agente", zap.Error(err))
		return fmt.Errorf("erro ao obter resposta da IA: %w", err)
	}

	// 6. Adicionar a resposta ao histórico
	a.cli.history = append(a.cli.history, models.Message{
		Role:    "assistant",
		Content: aiResponse,
	})

	// 7. Processar a resposta para extrair blocos de comando
	commandBlocks := a.extractCommandBlocks(aiResponse)

	// 8. Exibir a explicação geral e os blocos de comando
	a.displayResponseWithoutCommands(aiResponse, commandBlocks)

	// 9. Para cada bloco de comando, pedir confirmação e executar
	if len(commandBlocks) > 0 {
		a.handleCommandBlocks(ctx, commandBlocks)
	} else {
		fmt.Println("\nNenhum comando executável encontrado na resposta.")
	}

	return nil
}

// Extração de blocos de comandos para aceitar qualquer linguagem
func (a *AgentMode) extractCommandBlocks(response string) []CommandBlock {
	var commandBlocks []CommandBlock

	// Regex para encontrar blocos de código com o prefixo execute
	// Captura: 1=linguagem (qualquer uma), 2=comandos
	re := regexp.MustCompile("```execute:([a-zA-Z0-9_-]+)\\s*\n([\\s\\S]*?)```")
	matches := re.FindAllStringSubmatch(response, -1)

	// Obter descrições dos comandos
	lines := strings.Split(response, "\n")

	for _, match := range matches {
		if len(match) >= 3 {
			language := strings.TrimSpace(match[1])
			commands := strings.TrimSpace(match[2])

			// Tentar encontrar a descrição antes do bloco de código
			description := ""
			blockStartIndex := strings.Index(response, match[0])
			if blockStartIndex > 0 {
				// Procurar até 5 linhas antes do bloco para encontrar uma descrição
				blockLocation := strings.Count(response[:blockStartIndex], "\n")
				startLine := max(0, blockLocation-5)
				for i := startLine; i < blockLocation; i++ {
					if i < len(lines) && lines[i] != "" && !strings.HasPrefix(lines[i], "```") {
						description = lines[i]
						break
					}
				}
			}

			commandBlocks = append(commandBlocks, CommandBlock{
				Description: description,
				Commands:    strings.Split(commands, "\n"),
				Language:    language,
			})
		}
	}

	return commandBlocks
}

// displayResponseWithoutCommands exibe a resposta sem os blocos de comando
func (a *AgentMode) displayResponseWithoutCommands(response string, blocks []CommandBlock) {
	// Substituir os blocos de comando por marcadores
	displayResponse := response
	for i, block := range blocks {
		// Reconstruir o bloco original para substituição
		originalBlock := fmt.Sprintf("```execute:%s\n%s```",
			block.Language,
			strings.Join(block.Commands, "\n"))

		// Substituir por um marcador
		replacement := fmt.Sprintf("\n[Comando #%d: %s]\n", i+1, block.Description)
		displayResponse = strings.Replace(displayResponse, originalBlock, replacement, 1)
	}

	// Renderizar e exibir
	renderedResponse := a.cli.renderMarkdown(displayResponse)
	a.cli.typewriterEffect(fmt.Sprintf("\n%s:\n%s\n", a.cli.client.GetModelName(), renderedResponse), 2*time.Millisecond)
}

// handleCommandBlocks processa cada bloco de comando
func (a *AgentMode) handleCommandBlocks(ctx context.Context, blocks []CommandBlock) {
	fmt.Println("\n🤖 MODO AGENTE: Comandos sugeridos")
	fmt.Println("===============================")

	// Exibir todos os comandos disponíveis
	for i, block := range blocks {
		fmt.Printf("\n🔷 Comando #%d: %s\n", i+1, block.Description)
		fmt.Printf("  Tipo: %s\n", block.Language)
		fmt.Printf("  Comandos:\n")

		// Exibir os comandos
		for _, cmd := range block.Commands {
			fmt.Printf("    %s\n", cmd)
		}
	}

	// Fechar o liner temporariamente para liberar o terminal
	if a.cli.line != nil {
		a.cli.line.Close()
	}

	// Usar o método mais básico possível para ler a entrada
	fmt.Printf("\nDigite sua opção:\n")
	fmt.Printf("- Um número entre 1 e %d para executar esse comando\n", len(blocks))
	fmt.Printf("- 'a' para executar todos os comandos\n")
	//fmt.Printf("- 'eX' para editar o comando número X (por exemplo, 'e2' para editar o comando 2)\n")
	fmt.Printf("- 'q' para sair\n")
	fmt.Print("Sua escolha: ")

	var answer string
	fmt.Scan(&answer) // Usa Scan para garantir a leitura

	answer = strings.ToLower(strings.TrimSpace(answer))

	// Recriar o liner após a leitura
	a.cli.line = liner.NewLiner()
	a.cli.line.SetCtrlCAborts(true)

	if answer == "q" {
		fmt.Println("Saindo do modo agente.")
		return
	} else if answer == "a" {
		// Executar todos os comandos em sequência
		fmt.Println("\n⚠️ Executando todos os comandos em sequência...")
		for i, block := range blocks {
			fmt.Printf("\n🚀 Executando comando #%d:\n", i+1)
			a.executeCommands(ctx, block)
		}
		return
	} else {
		// Tentar interpretar como número de comando
		cmdNum, err := strconv.Atoi(answer)
		if err != nil || cmdNum < 1 || cmdNum > len(blocks) {
			fmt.Println("Opção inválida.")
			return
		}

		// Executar o comando selecionado
		selectedBlock := blocks[cmdNum-1]
		a.executeCommands(ctx, selectedBlock)
	}

	fmt.Println("\n✅ Processamento de comandos concluído.")
}

// executeCommands executa os comandos do bloco
func (a *AgentMode) executeCommands(ctx context.Context, block CommandBlock) {
	fmt.Printf("\n🚀 Executando comandos (tipo: %s):\n", block.Language)
	fmt.Println("---------------------------------------")

	// Mostrar um indicador de progresso
	fmt.Printf("⌛ Processando: %s\n\n", block.Description)

	// Determinar o shell a usar
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}

	// Executar cada comando separadamente para melhor feedback
	for i, cmd := range block.Commands {
		if cmd == "" {
			continue // Pular linhas vazias
		}

		fmt.Printf("⚙️ Comando %d/%d: %s\n", i+1, len(block.Commands), cmd)

		// Criar e configurar o comando
		command := exec.CommandContext(ctx, shell, "-c", cmd)
		command.Stdin = os.Stdin
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr

		// Executar o comando
		err := command.Run()
		if err != nil {
			fmt.Printf("❌ Erro: %v\n\n", err)
		} else {
			fmt.Printf("✓ Executado com sucesso\n\n")
		}
	}

	fmt.Println("---------------------------------------")
	fmt.Println("Execução concluída.")
}

// max retorna o maior entre dois inteiros
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
