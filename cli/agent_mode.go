package cli

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"github.com/peterh/liner"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
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

// safePrompt cria um novo liner, obtém input e fecha com segurança
func (a *AgentMode) safePrompt(prompt string) (string, error) {
	// Criar um novo liner para cada prompt
	line := liner.NewLiner()
	defer line.Close()

	// Configurar o liner
	line.SetCtrlCAborts(true)

	// Opcional: configurar completador
	if a.cli.completer != nil {
		line.SetCompleter(a.cli.completer)
	}

	// Opcional: carregar histórico recente
	for i := 0; i < len(a.cli.commandHistory) && i < 50; i++ {
		idx := len(a.cli.commandHistory) - 1 - i
		if idx >= 0 {
			line.AppendHistory(a.cli.commandHistory[idx])
		}
	}

	// Obter input
	input, err := line.Prompt(prompt)
	if err != nil {
		return "", err
	}

	// Adicionar ao histórico global
	if input != "" {
		a.cli.commandHistory = append(a.cli.commandHistory, input)
	}

	return input, nil
}

// Função de fallback para quando o prompt seguro falhar
func (a *AgentMode) fallbackPrompt(prompt string) string {
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	resp, _ := reader.ReadString('\n')
	return strings.TrimSpace(resp)
}

// getInput obtém entrada do usuário de forma segura
func (a *AgentMode) getInput(prompt string) string {
	// Usar o liner da instância principal em vez de criar um novo
	if a.cli.line != nil {
		input, err := a.cli.line.Prompt(prompt)
		if err != nil {
			if err == liner.ErrPromptAborted {
				return ""
			}
			// Fallback para método simples em caso de erro
			a.logger.Warn("Erro ao usar liner, usando fallback", zap.Error(err))
			return a.fallbackPrompt(prompt)
		}

		// Adicionar ao histórico global se não estiver vazio
		if input != "" {
			a.cli.line.AppendHistory(input)
			a.cli.commandHistory = append(a.cli.commandHistory, input)
		}

		return input
	} else {
		// Se não houver liner disponível, usar o método fallback
		return a.fallbackPrompt(prompt)
	}
}

// CommandBlock representa um bloco de comandos executáveis
type CommandBlock struct {
	Description string   // Descrição do que o comando faz
	Commands    []string // Os comandos a serem executados
	Language    string   // Linguagem (bash, python, etc.)
}

type CommandOutput struct {
	CommandBlock CommandBlock
	Output       string
	ErrorMsg     string
}

var dangerousPatterns = []string{
	`(?i)rm\s+-rf\s+`,             // rm -rf
	`(?i)rm\s+--no-preserve-root`, // rm --no-preserve-root
	`(?i)dd\s+if=`,                // dd
	`(?i)mkfs\w*\s+`,              // mkfs
	`(?i)shutdown(\s+|$)`,         // shutdown
	`(?i)reboot(\s+|$)`,           // reboot
	`(?i)init\s+0`,                // init 0
	`(?i)curl\s+[^\|;]*\|\s*sh`,   // pipe a shell
	`(?i)wget\s+[^\|;]*\|\s*sh`,
	`(?i)curl\s+[^\|;]*\|\s*bash`,
	`(?i)wget\s+[^\|;]*\|\s*bash`,
	`(?i)\bsudo\b.*`,          // comando usando sudo
	`(?i)\bdrop\s+database\b`, // apagar bancos
	`(?i)\bmkfs\b`,            // formatar partição
	`(?i)\buserdel\b`,         // deletar usuário
	`(?i)\bchmod\s+777\s+/.*`, // chmod 777 /
}

func isDangerous(cmd string) bool {
	for _, pattern := range dangerousPatterns {
		re := regexp.MustCompile(pattern)
		if re.MatchString(cmd) {
			return true
		}
	}
	return false
}

// Run inicia o modo agente com uma pergunta do usuário
func (a *AgentMode) Run(ctx context.Context, query string, additionalContext string) error {
	// 1. Enviar a pergunta para a LLM com instruções específicas sobre formato de resposta
	systemInstruction := `Você é um assistente de linha de comando que ajuda o usuário a executar tarefas no sistema.
            Quando o usuário pede para realizar uma tarefa, analise o problema e sugira o melhor comando que possam resolver a questão.
    
            Antes de sugerir comandos destrutivos (como rm -rf, dd, mkfs, drop database, etc), coloque explicação e peça uma confirmação explícita do usuário.
    
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

// getMultilineInput obtém entrada de múltiplas linhas do usuário
// Suporta:
// - ENTER vazio na primeira linha para continuar sem contexto
// - "." sozinho em uma linha para finalizar a entrada
// - Control+D (EOF) para finalizar a entrada
func (a *AgentMode) getMultilineInput(prompt string) string {
	fmt.Print(prompt)
	fmt.Println("(Digite '.' sozinho em uma linha para finalizar)")
	fmt.Println("(Pressione ENTER sem digitar texto para continuar sem contexto)")
	fmt.Println("(Ou use Control+D para finalizar a entrada)")

	// Fechar o liner temporariamente
	if a.cli.line != nil {
		a.cli.line.Close()
		a.cli.line = nil
	}

	var lines []string
	reader := bufio.NewReader(os.Stdin)
	firstLine := true

	for {
		// Ler uma linha
		line, err := reader.ReadString('\n')

		// Tratar EOF (Control+D)
		if err != nil {
			if len(lines) > 0 {
				fmt.Println("Entrada finalizada com Control+D")
				break
			} else {
				fmt.Println("Continuando sem adicionar contexto (Control+D)")
				lines = nil
				break
			}
		}

		// Remover quebra de linha
		line = strings.TrimRight(line, "\r\n")

		// Verificar primeira linha vazia (apenas ENTER)
		if firstLine && line == "" {
			fmt.Println("Continuando sem adicionar contexto.")
			lines = nil
			break
		}
		firstLine = false

		// Verificar linha com apenas "."
		if line == "." {
			fmt.Println("Entrada finalizada com '.'")
			break
		}

		// Adicionar linha ao buffer
		lines = append(lines, line)
	}

	// Restaurar o liner
	newLiner := liner.NewLiner()
	newLiner.SetCtrlCAborts(true)

	// Recarregar histórico
	for _, cmd := range a.cli.commandHistory {
		newLiner.AppendHistory(cmd)
	}

	// Restaurar completador
	if a.cli.completer != nil {
		newLiner.SetCompleter(a.cli.completer)
	}

	a.cli.line = newLiner

	// Se não tivermos linhas, retornar string vazia
	if lines == nil || len(lines) == 0 {
		return ""
	}

	return strings.Join(lines, "\n")
}

// requestLLMContinuationWithContext reenvia o contexto/output + contexto adicional do usuário para a LLM
func (a *AgentMode) requestLLMContinuationWithContext(ctx context.Context, previousCommand, output, stderr, userContext string) ([]CommandBlock, error) {
	var prompt strings.Builder

	prompt.WriteString("O comando sugerido anteriormente foi:\n")
	prompt.WriteString(previousCommand)
	prompt.WriteString("\n\nO resultado (stdout) foi:\n")
	prompt.WriteString(output)

	if stderr != "" {
		prompt.WriteString("\n\nO erro (stderr) foi:\n")
		prompt.WriteString(stderr)
	}

	if userContext != "" {
		prompt.WriteString("\n\nContexto adicional fornecido pelo usuário:\n")
		prompt.WriteString(userContext)
	}

	prompt.WriteString("\n\nPor favor, sugira uma correção ou próximos passos baseados no resultado e no contexto fornecido. ")
	prompt.WriteString("Forneça comandos executáveis no formato apropriado.")

	// Adiciona o prompt como novo turno "user"
	a.cli.history = append(a.cli.history, models.Message{
		Role:    "user",
		Content: prompt.String(),
	})

	a.cli.animation.ShowThinkingAnimation(a.cli.client.GetModelName())
	aiResponse, err := a.cli.client.SendPrompt(ctx, prompt.String(), a.cli.history)
	a.cli.animation.StopThinkingAnimation()
	if err != nil {
		fmt.Println("❌ Erro ao pedir continuação à IA:", err)
		return nil, err
	}

	// Adiciona resposta da IA ao histórico
	a.cli.history = append(a.cli.history, models.Message{
		Role:    "assistant",
		Content: aiResponse,
	})

	// Processa normalmente (extrai comandos, mostra explicação, etc)
	blocks := a.extractCommandBlocks(aiResponse)
	a.displayResponseWithoutCommands(aiResponse, blocks)
	return blocks, nil
}

// handleCommandBlocks processa cada bloco de comando
func (a *AgentMode) handleCommandBlocks(ctx context.Context, blocks []CommandBlock) {
	fmt.Println("\n🤖 MODO AGENTE: Comandos sugeridos")
	fmt.Println("===============================")

	outputs := make([]*CommandOutput, len(blocks))

	for {
		// Mostra os comandos disponíveis
		for i, block := range blocks {
			fmt.Printf("\n🔷 Comando #%d: %s\n", i+1, block.Description)
			fmt.Printf("  Tipo: %s\n", block.Language)
			fmt.Printf("  Comandos:\n")
			for _, cmd := range block.Commands {
				fmt.Printf("    %s\n", cmd)
			}
		}

		fmt.Printf("\nDigite sua opção:\n")
		fmt.Printf("- Um número entre 1 e %d para executar esse comando\n", len(blocks))
		fmt.Printf("- 'a' para executar todos os comandos\n")
		fmt.Printf("- 'eN' para editar o comando N antes de rodar (ex: e2)\n")
		fmt.Printf("- 'tN' para simular (dry-run) o comando N (ex: t2)\n")
		fmt.Printf("- 'cN' para pedir continuação à IA usando a saída do comando N (ex: c2)\n")
		fmt.Printf("- 'aCN' para adicionar contexto à saída do comando N e pedir continuação (ex: aC2)\n")
		fmt.Printf("- 'q' para sair\n")

		// Usar o prompt seguro para a entrada
		answer := a.getInput("Sua escolha: ")
		answer = strings.ToLower(strings.TrimSpace(answer))

		switch {
		case answer == "q":
			fmt.Println("Saindo do modo agente.")
			return

		case answer == "a":
			hasDanger := false
			for _, b := range blocks {
				for _, c := range b.Commands {
					if isDangerous(c) {
						hasDanger = true
						break
					}
				}
			}
			if hasDanger {
				fmt.Println("⚠️ AVISO: Um ou mais comandos a executar são potencialmente perigosos (destrutivos ou invasivos).")
				fmt.Println("Confira comandos individuais antes de aprovar execução em lote!")
			}

			// Resetar o estado do terminal
			cmd := exec.Command("stty", "sane")
			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			cmd.Run() // Ignoramos erros aqui propositalmente

			// Solicitar confirmação diretamente
			fmt.Print("\n⚠️ Executar todos os comandos em sequência? (s/N): ")

			// Ler resposta
			reader := bufio.NewReader(os.Stdin)
			confirmationInput, _ := reader.ReadString('\n')
			confirmation := strings.ToLower(strings.TrimSpace(confirmationInput))

			// Verificar resposta explicitamente
			if confirmation != "s" {
				fmt.Println("Execução em lote cancelada.")
				continue
			}

			// Adicionar log explícito para depuração
			fmt.Println("\n⚠️ Confirmação recebida: '" + confirmation + "'")
			fmt.Println("⚠️ Executando todos os comandos em sequência...")

			// Executar os comandos um por um, com logs detalhados
			for i, block := range blocks {
				fmt.Printf("\n🚀 Executando comando #%d:\n", i+1)
				fmt.Printf("  Tipo: %s\n", block.Language)
				for j, cmd := range block.Commands {
					fmt.Printf("  Comando %d/%d: %s\n", j+1, len(block.Commands), cmd)
				}

				// Executar o bloco e capturar a saída
				outStr, errStr := a.executeCommandsWithOutput(ctx, block)

				// Armazenar os resultados
				outputs[i] = &CommandOutput{
					CommandBlock: block,
					Output:       outStr,
					ErrorMsg:     errStr,
				}

				// Log após execução
				fmt.Printf("✅ Comando #%d concluído\n", i+1)
			}

			fmt.Println("\n✅ Todos os comandos foram executados.")

		case strings.HasPrefix(answer, "e"):
			cmdNumStr := strings.TrimPrefix(answer, "e")
			cmdNum, err := strconv.Atoi(cmdNumStr)
			if err != nil || cmdNum < 1 || cmdNum > len(blocks) {
				fmt.Println("Número de comando inválido para editar.")
				continue
			}
			edited, err := a.editCommandBlock(blocks[cmdNum-1])
			if err != nil {
				fmt.Println("Erro ao editar comando:", err)
				continue
			}
			editedBlock := blocks[cmdNum-1]
			editedBlock.Commands = edited
			outStr, errStr := a.executeCommandsWithOutput(ctx, editedBlock)
			outputs[cmdNum-1] = &CommandOutput{
				CommandBlock: editedBlock,
				Output:       outStr,
				ErrorMsg:     errStr,
			}

		case strings.HasPrefix(answer, "t"):
			cmdNumStr := strings.TrimPrefix(answer, "t")
			cmdNum, err := strconv.Atoi(cmdNumStr)
			if err != nil || cmdNum < 1 || cmdNum > len(blocks) {
				fmt.Println("Número de comando inválido para simular.")
				continue
			}
			a.simulateCommandBlock(ctx, blocks[cmdNum-1])

			execNow := a.getInput("Deseja executar este comando agora? (s/N): ")
			if strings.ToLower(strings.TrimSpace(execNow)) == "s" {
				outStr, errStr := a.executeCommandsWithOutput(ctx, blocks[cmdNum-1])
				outputs[cmdNum-1] = &CommandOutput{
					CommandBlock: blocks[cmdNum-1],
					Output:       outStr,
					ErrorMsg:     errStr,
				}
			} else {
				fmt.Println("Simulação concluída, comando NÃO executado.")
			}

		case strings.HasPrefix(answer, "ac"):
			cmdNumStr := strings.TrimPrefix(answer, "ac")
			cmdNum, err := strconv.Atoi(cmdNumStr)
			if err != nil || cmdNum < 1 || cmdNum > len(blocks) {
				fmt.Println("Número inválido para adicionar contexto.")
				continue
			}
			if outputs[cmdNum-1] == nil {
				fmt.Println("Este comando ainda não foi executado, portanto não há saída para adicionar contexto.")
				continue
			}

			// Mostrar o output para o usuário saber o que está contextualizando
			fmt.Println("\n📋 Saída do comando que você está contextualizando:")
			fmt.Println("---------------------------------------")
			fmt.Print(outputs[cmdNum-1].Output)
			fmt.Println("---------------------------------------")

			// Obter o contexto adicional do usuário
			userContext := a.getMultilineInput("Digite seu contexto adicional:\n")

			// Se o usuário cancelou ou não forneceu contexto
			if userContext == "" {
				fmt.Println("Continuando sem contexto adicional...")

				// Chamar método para tratar a continuação sem contexto adicional
				newBlocks, err := a.requestLLMContinuationWithContext(
					ctx,
					strings.Join(blocks[cmdNum-1].Commands, "\n"),
					outputs[cmdNum-1].Output,
					outputs[cmdNum-1].ErrorMsg,
					"", // Contexto vazio
				)
				if err != nil {
					fmt.Println("Erro ao pedir continuação à IA:", err)
					continue
				}
				if len(newBlocks) > 0 {
					blocks = newBlocks                            // troca para os novos comandos da IA!
					outputs = make([]*CommandOutput, len(blocks)) // Reset outputs para o novo tamanho
					break                                         // Sai desse loop for & reinicia com novos comandos
				} else {
					fmt.Println("\nNenhum comando sugerido pela IA na resposta.")
				}
			} else {
				fmt.Println("\nContexto recebido! Enviando para a IA...")

				// Chamar método para tratar a continuação com contexto
				newBlocks, err := a.requestLLMContinuationWithContext(
					ctx,
					strings.Join(blocks[cmdNum-1].Commands, "\n"),
					outputs[cmdNum-1].Output,
					outputs[cmdNum-1].ErrorMsg,
					userContext,
				)
				if err != nil {
					fmt.Println("Erro ao pedir continuação à IA:", err)
					continue
				}
				if len(newBlocks) > 0 {
					blocks = newBlocks                            // troca para os novos comandos da IA!
					outputs = make([]*CommandOutput, len(blocks)) // Reset outputs para o novo tamanho
					break                                         // Sai desse loop for & reinicia com novos comandos
				} else {
					fmt.Println("\nNenhum comando sugerido pela IA na resposta.")
				}
			}

		case strings.HasPrefix(answer, "c"):
			cmdNumStr := strings.TrimPrefix(answer, "c")
			cmdNum, err := strconv.Atoi(cmdNumStr)
			if err != nil || cmdNum < 1 || cmdNum > len(blocks) {
				fmt.Println("Número inválido para continuação.")
				continue
			}
			if outputs[cmdNum-1] == nil {
				fmt.Println("Este comando ainda não foi executado, portanto não há saída para enviar à IA.")
				continue
			}

			newBlocks, err := a.requestLLMContinuation(
				ctx,
				strings.Join(blocks[cmdNum-1].Commands, "\n"),
				strings.Join(blocks[cmdNum-1].Commands, "\n"),
				outputs[cmdNum-1].Output,
				outputs[cmdNum-1].ErrorMsg,
			)
			if err != nil {
				fmt.Println("Erro ao pedir continuação à IA:", err)
				continue
			}
			if len(newBlocks) > 0 {
				blocks = newBlocks                            // troca para os novos comandos da IA!
				outputs = make([]*CommandOutput, len(blocks)) // Reset outputs para o novo tamanho
				break                                         // Sai desse loop for & reinicia com novos comandos
			} else {
				fmt.Println("\nNenhum comando sugerido pela IA na resposta.")
			}

		default:
			cmdNum, err := strconv.Atoi(answer)
			if err != nil || cmdNum < 1 || cmdNum > len(blocks) {
				fmt.Println("Opção inválida.")
				continue
			}
			outStr, errStr := a.executeCommandsWithOutput(ctx, blocks[cmdNum-1])
			outputs[cmdNum-1] = &CommandOutput{
				CommandBlock: blocks[cmdNum-1],
				Output:       outStr,
				ErrorMsg:     errStr,
			}
		}
	}
}

// executeCommandsWithOutput executa todos os comandos do bloco (1 a 1), imprime e retorna o output total e último erro.
func (a *AgentMode) executeCommandsWithOutput(ctx context.Context, block CommandBlock) (string, string) {
	var allOutput strings.Builder
	var lastError string

	// Adicionar apenas os cabeçalhos iniciais à string de saída
	allOutput.WriteString(fmt.Sprintf("\n🚀 Executando comandos (tipo: %s):\n", block.Language))
	allOutput.WriteString("---------------------------------------\n")
	allOutput.WriteString(fmt.Sprintf("⌛ Processando: %s\n\n", block.Description))

	// Imprimir os cabeçalhos iniciais para o usuário ver
	fmt.Printf("\n🚀 Executando comandos (tipo: %s):\n", block.Language)
	fmt.Println("---------------------------------------")
	fmt.Printf("⌛ Processando: %s\n\n", block.Description)

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}

	for i, cmd := range block.Commands {
		if cmd == "" {
			continue
		}

		// Verificar se o comando tem a flag de interatividade
		isInteractive := false
		if strings.HasSuffix(cmd, " --interactive") {
			cmd = strings.TrimSuffix(cmd, " --interactive")
			isInteractive = true
		} else if strings.Contains(cmd, "#interactive") {
			cmd = strings.Replace(cmd, "#interactive", "", -1)
			cmd = strings.TrimSpace(cmd)
			isInteractive = true
		} else {
			// Verificar se é um comando provavelmente interativo
			isInteractive = isLikelyInteractiveCommand(cmd)
		}

		// Checagem de comando perigoso
		if isDangerous(cmd) {
			confirmPrompt := "Este comando é potencialmente perigoso e pode causar danos ao sistema.\nSe tem certeza que deseja executar, digite exatamente: 'sim, quero executar conscientemente' \n Confirma ? (Digite exatamente para confirmar): "
			confirm := a.getCriticalInput(confirmPrompt)

			if confirm != "sim, quero executar conscientemente" {
				outText := "Execução do comando perigoso ABORTADA pelo agente.\n"
				allOutput.WriteString(outText)
				fmt.Print(outText)
				continue
			}

			fmt.Println("⚠️ Confirmação recebida. Executando comando perigoso...")
		}

		header := fmt.Sprintf("⚙️ Comando %d/%d: %s\n", i+1, len(block.Commands), cmd)
		allOutput.WriteString(header)
		fmt.Print(header)

		// Se o comando não foi explicitamente marcado como interativo
		// mas pode ser interativo, perguntar ao usuário
		if !isInteractive && mightBeInteractive(cmd) {
			isInteractive = a.askUserIfInteractive(cmd)
		}

		if isInteractive {
			// Para comandos interativos, usamos uma abordagem diferente
			outText := "🖥️ Executando comando interativo. O controle será passado para o comando.\n"
			outText += "Quando o comando terminar, você retornará ao modo agente.\n"
			outText += "Pressione Ctrl+C para interromper o comando se necessário.\n\n"

			allOutput.WriteString(outText)
			fmt.Print(outText)

			// Liberar o terminal e executar o comando interativo
			err := a.executeInteractiveCommand(ctx, shell, cmd)

			if err != nil {
				errMsg := fmt.Sprintf("❌ Erro ao executar comando interativo: %v\n\n", err)
				allOutput.WriteString(errMsg)
				fmt.Print(errMsg)
				lastError = err.Error()
			} else {
				okMsg := "✓ Comando interativo executado com sucesso\n\n"
				allOutput.WriteString(okMsg)
				fmt.Print(okMsg)
			}
		} else {
			// Para comandos não interativos, capturar a saída normalmente
			output, err := a.captureCommandOutput(ctx, shell, []string{"-c", cmd})

			outText := "📝 Saída do comando (stdout/stderr):\n" + string(output)
			allOutput.WriteString(outText)
			fmt.Print(outText)

			if err != nil {
				errMsg := fmt.Sprintf("❌ Erro: %v\n\n", err)
				allOutput.WriteString(errMsg)
				fmt.Print(errMsg)
				lastError = err.Error()
			} else {
				okMsg := "✓ Executado com sucesso\n\n"
				allOutput.WriteString(okMsg)
				fmt.Print(okMsg)
			}
		}
	}

	finalMsg := "---------------------------------------\nExecução concluída.\n"
	allOutput.WriteString(finalMsg)
	fmt.Print(finalMsg)

	// Retornar a saída acumulada e o último erro
	return allOutput.String(), lastError
}

// executeInteractiveCommand executa um comando interativo passando o controle do terminal
func (a *AgentMode) executeInteractiveCommand(ctx context.Context, shell string, command string) error {
	// Fechar o liner para liberar o terminal
	if a.cli.line != nil {
		a.cli.line.Close()
		a.cli.line = nil
	}

	// Restaurar o terminal para o modo "sane"
	sttyCmd := exec.Command("stty", "sane")
	sttyCmd.Stdin = os.Stdin
	sttyCmd.Stdout = os.Stdout
	sttyCmd.Run() // Ignoramos erros aqui propositalmente

	// Obter o caminho do arquivo de configuração do shell
	shellConfigPath := a.getShellConfigPath(shell)

	// Construir o comando com shell
	var shellCommand string
	if shellConfigPath != "" {
		shellCommand = fmt.Sprintf("source %s 2>/dev/null || true; %s", shellConfigPath, command)
	} else {
		shellCommand = command
	}

	// Criar o comando com conexão direta ao terminal
	cmd := exec.CommandContext(ctx, shell, "-c", shellCommand)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Executar o comando (bloqueante)
	err := cmd.Run()

	// Após o comando terminar, restaurar o terminal para o modo "sane" novamente
	sttyCmd = exec.Command("stty", "sane")
	sttyCmd.Stdin = os.Stdin
	sttyCmd.Stdout = os.Stdout
	sttyCmd.Run() // Ignoramos erros aqui propositalmente

	fmt.Println("\nComando interativo finalizado. Retornando ao modo agente...")

	// Reinstanciar o liner
	if a.cli.line == nil {
		newLiner := liner.NewLiner()
		newLiner.SetCtrlCAborts(true)

		// Recarregar histórico
		for _, cmd := range a.cli.commandHistory {
			newLiner.AppendHistory(cmd)
		}

		// Restaurar completador
		if a.cli.completer != nil {
			newLiner.SetCompleter(a.cli.completer)
		}

		a.cli.line = newLiner
	}

	return err
}

// getShellConfigPath obtém o caminho de configuração para o shell especificado
func (a *AgentMode) getShellConfigPath(shell string) string {
	shellName := filepath.Base(shell)
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "" // Retorna vazio se não puder determinar o home
	}

	switch shellName {
	case "bash":
		return filepath.Join(homeDir, ".bashrc")
	case "zsh":
		return filepath.Join(homeDir, ".zshrc")
	case "fish":
		return filepath.Join(homeDir, ".config", "fish", "config.fish")
	default:
		return "" // Retorna vazio para shells desconhecidos
	}
}

// isLikelyInteractiveCommand verifica se um comando provavelmente é interativo
func isLikelyInteractiveCommand(cmd string) bool {
	// Lista de comandos conhecidos por serem interativos
	interactiveCommands := []string{
		"top", "htop", "nettop", "iotop", "vi", "vim", "nano", "emacs", "less",
		"more", "tail -f", "watch", "ssh", "mysql", "psql", "sqlite3", "python",
		"ipython", "node", "irb", "R", "mongo", "redis-cli", "sqlplus", "ftp",
		"sftp", "telnet", "screen", "tmux", "ncdu", "mc", "ranger", "irssi",
		"weechat", "mutt", "lynx", "links", "w3m", "docker exec -it", "kubectl exec -it",
	}

	cmdLower := strings.ToLower(cmd)

	// Verificar comandos conhecidos
	for _, interactive := range interactiveCommands {
		if strings.HasPrefix(cmdLower, interactive+" ") || cmdLower == interactive {
			return true
		}
	}

	// Verificar por flags que indicam interatividade
	interactiveFlags := []string{
		"-i ", "--interactive", "-t ", "--tty",
	}

	for _, flag := range interactiveFlags {
		if strings.Contains(cmdLower, flag) {
			return true
		}
	}

	return false
}

// mightBeInteractive verifica se um comando pode ser interativo mas não foi detectado automaticamente
func mightBeInteractive(cmd string) bool {
	// Lista de padrões que podem indicar interatividade, mas não são certeza
	possiblyInteractivePatterns := []string{
		"ping", "traceroute", "nc ", "netcat", "telnet", "ftp", "ssh", "console",
		"monitor", "interactive", "curses", "dialog", "whiptail", "menu",
	}

	cmdLower := strings.ToLower(cmd)
	for _, pattern := range possiblyInteractivePatterns {
		if strings.Contains(cmdLower, pattern) {
			return true
		}
	}

	return false
}

// getCriticalInput obtém entrada do usuário para decisões críticas
func (a *AgentMode) getCriticalInput(prompt string) string {
	// Primeiro, tentar liberar o terminal se estiver usando liner
	if a.cli.line != nil {
		a.cli.line.Close()
		a.cli.line = nil
	}

	// Resetar o estado do terminal
	cmd := exec.Command("stty", "sane")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Run() // Ignoramos erros aqui propositalmente

	// Exibir o prompt com formatação clara
	fmt.Print("\n")
	fmt.Print(prompt)

	// Ler diretamente do stdin
	reader := bufio.NewReader(os.Stdin)
	response, _ := reader.ReadString('\n')
	response = strings.TrimSpace(response)

	fmt.Println() // Adicionar uma linha em branco após a resposta

	// Reinstanciar o liner se necessário
	if a.cli.line == nil {
		newLiner := liner.NewLiner()
		newLiner.SetCtrlCAborts(true)

		// Recarregar histórico
		for _, cmd := range a.cli.commandHistory {
			newLiner.AppendHistory(cmd)
		}

		// Restaurar completador
		if a.cli.completer != nil {
			newLiner.SetCompleter(a.cli.completer)
		}

		a.cli.line = newLiner
	}

	return response
}

// askUserIfInteractive pergunta ao usuário se um comando deve ser executado em modo interativo
func (a *AgentMode) askUserIfInteractive(cmd string) bool {
	prompt := fmt.Sprintf("O comando '%s' pode ser interativo. Executar em modo interativo? (s/N): ", cmd)
	response := a.getCriticalInput(prompt)
	return strings.HasPrefix(strings.ToLower(response), "s")
}

// simulateCommandBlock tenta rodar os comandos de um bloco em modo "simulado"
func (a *AgentMode) simulateCommandBlock(ctx context.Context, block CommandBlock) {
	fmt.Printf("\n🔎 Simulando comandos (tipo: %s):\n", block.Language)
	fmt.Println("---------------------------------------")

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}

	for i, cmd := range block.Commands {
		if cmd == "" {
			continue
		}
		fmt.Printf("🔸 Dry-run %d/%d: %s\n", i+1, len(block.Commands), cmd)
		// Para shell, prefixa-"echo"
		simCmd := fmt.Sprintf("echo '[dry-run] Vai executar: %s'", cmd)
		// para outros tipos: analise e tente "simular"
		if block.Language == "shell" {
			out, err := a.captureCommandOutput(ctx, shell, []string{"-c", simCmd})
			fmt.Println(string(out))
			if err != nil {
				fmt.Printf("❗ Dry-run falhou: %v\n", err)
			}
		} else if block.Language == "kubernetes" && strings.Contains(cmd, "apply") {
			// Exemplo: kubectl apply --dry-run=client
			cmdDry := cmd + " --dry-run=client"
			out, err := a.captureCommandOutput(ctx, shell, []string{"-c", cmdDry})
			fmt.Println(string(out))
			if err != nil {
				fmt.Printf("❗ Dry-run falhou: %v\n", err)
			}
		} else {
			// padrão apenas echo
			out, _ := a.captureCommandOutput(ctx, shell, []string{"-c", "echo '[dry-run] " + cmd + "'"})
			fmt.Println(string(out))
		}
	}
	fmt.Println("---------------------------------------")
}

// captureCommandOutput executa comando e captura stdout+stderr
func (a *AgentMode) captureCommandOutput(ctx context.Context, shell string, args []string) ([]byte, error) {
	var outBuf bytes.Buffer
	var errBuf bytes.Buffer
	cmd := exec.CommandContext(ctx, shell, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	output := outBuf.Bytes()
	if errBuf.Len() > 0 {
		output = append(output, []byte("\n[stderr]:\n")...)
		output = append(output, errBuf.Bytes()...)
	}
	return output, err
}

// editCommandBlock abre o(s) comando(s) em um editor e retorna o texto editado
func (a *AgentMode) editCommandBlock(block CommandBlock) ([]string, error) {
	choice := a.getInput("Editar no terminal (t) ou em editor externo (e)? [t/e]: ")
	choice = strings.ToLower(strings.TrimSpace(choice))

	if choice == "t" {
		// Editar cada comando individualmente no terminal
		editedCommands := make([]string, len(block.Commands))

		for i, cmd := range block.Commands {
			if cmd == "" {
				continue
			}

			prompt := fmt.Sprintf("Comando %d/%d (%s): ", i+1, len(block.Commands), block.Language)
			edited := a.getInput(prompt)

			if edited == "" {
				edited = cmd // Manter o comando original se o usuário não inserir nada
			}

			editedCommands[i] = edited
		}

		return editedCommands, nil
	}

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vim"
	}
	tmpfile, err := ioutil.TempFile("", "agent-edit-*.sh")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmpfile.Name())

	content := strings.Join(block.Commands, "\n")
	if _, err := tmpfile.Write([]byte(content)); err != nil {
		return nil, err
	}
	if err := tmpfile.Close(); err != nil {
		return nil, err
	}

	cmd := exec.Command(editor, tmpfile.Name())
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, err
	}

	edited, err := ioutil.ReadFile(tmpfile.Name())
	if err != nil {
		return nil, err
	}

	lines := strings.Split(strings.ReplaceAll(string(edited), "\r\n", "\n"), "\n")
	return lines, nil
}

// requestLLMContinuation reenvia o contexto/output para a LLM gerar novo comando
func (a *AgentMode) requestLLMContinuation(ctx context.Context, userQuery, previousCommand, output, stderr string) ([]CommandBlock, error) {
	retryPrompt := fmt.Sprintf(
		`O comando sugerido anteriormente foi:
        %s
        
        O resultado (stdout) foi:
        %s
        
        O erro (stderr) foi:
        %s
        
        Por favor, sugira uma correção OU explique o erro e proponha um novo bloco de comando. Não repita comandos que já claramente deram erro sem modificação.
        
        Se necessário, peça informações extras ao usuário.`, previousCommand, output, stderr)

	// Adiciona o retryPrompt como novo turno "user"
	a.cli.history = append(a.cli.history, models.Message{
		Role:    "user",
		Content: retryPrompt,
	})

	a.cli.animation.ShowThinkingAnimation(a.cli.client.GetModelName())
	aiResponse, err := a.cli.client.SendPrompt(ctx, retryPrompt, a.cli.history)
	a.cli.animation.StopThinkingAnimation()
	if err != nil {
		fmt.Println("❌ Erro ao pedir continuação à IA:", err)
		return nil, err
	}

	// Adiciona resposta da IA ao histórico
	a.cli.history = append(a.cli.history, models.Message{
		Role:    "assistant",
		Content: aiResponse,
	})

	// Processa normalmente (extrai comandos, mostra explicação, etc)
	blocks := a.extractCommandBlocks(aiResponse)
	a.displayResponseWithoutCommands(aiResponse, blocks)
	return blocks, nil
}

// max retorna o maior entre dois inteiros
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
