package cli

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
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

func askLine(msg string) string {
	if msg != "" {
		fmt.Print(msg)
	}
	reader := bufio.NewReader(os.Stdin)
	resp, _ := reader.ReadString('\n')
	return strings.TrimSpace(resp)
}

// CommandBlock representa um bloco de comandos executáveis
type CommandBlock struct {
	Description string   // Descrição do que o comando faz
	Commands    []string // Os comandos a serem executados
	Language    string   // Linguagem (bash, python, etc.)
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

// handleCommandBlocks processa cada bloco de comando
func (a *AgentMode) handleCommandBlocks(ctx context.Context, blocks []CommandBlock) {
	fmt.Println("\n🤖 MODO AGENTE: Comandos sugeridos")
	fmt.Println("===============================")

	type CommandOutput struct {
		CommandBlock CommandBlock
		Output       string
		ErrorMsg     string
	}

	outputs := make([]*CommandOutput, len(blocks))

	// Seu menu agora é um laço, pode receber atualizações de blocks e outputs no ciclo
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

		// Feche o liner se existir, nunca reabra aqui!
		if a.cli.line != nil {
			a.cli.line.Close()
		}

		fmt.Printf("\nDigite sua opção:\n")
		fmt.Printf("- Um número entre 1 e %d para executar esse comando\n", len(blocks))
		fmt.Printf("- 'a' para executar todos os comandos\n")
		fmt.Printf("- 'eN' para editar o comando N antes de rodar (ex: e2)\n")
		fmt.Printf("- 'tN' para simular (dry-run) o comando N (ex: t2)\n")
		fmt.Printf("- 'cN' para pedir continuação à IA usando a saída do comando N (ex: c2)\n")
		fmt.Printf("- 'q' para sair\n")
		answer := strings.ToLower(askLine("Sua escolha: "))

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
			fmt.Println("\n⚠️ Executando todos os comandos em sequência...")
			for i, block := range blocks {
				fmt.Printf("\n🚀 Executando comando #%d:\n", i+1)
				outStr, errStr := a.executeCommandsWithOutput(ctx, block)
				outputs[i] = &CommandOutput{
					CommandBlock: block,
					Output:       outStr,
					ErrorMsg:     errStr,
				}
				fmt.Print(outStr)
			}

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
			fmt.Print(outStr)

		case strings.HasPrefix(answer, "t"):
			cmdNumStr := strings.TrimPrefix(answer, "t")
			cmdNum, err := strconv.Atoi(cmdNumStr)
			if err != nil || cmdNum < 1 || cmdNum > len(blocks) {
				fmt.Println("Número de comando inválido para simular.")
				continue
			}
			a.simulateCommandBlock(ctx, blocks[cmdNum-1])
			execNow := strings.ToLower(askLine("Deseja executar este comando agora? (s/N): "))
			if execNow == "s" {
				outStr, errStr := a.executeCommandsWithOutput(ctx, blocks[cmdNum-1])
				outputs[cmdNum-1] = &CommandOutput{
					CommandBlock: blocks[cmdNum-1],
					Output:       outStr,
					ErrorMsg:     errStr,
				}
				fmt.Print(outStr)
			} else {
				fmt.Println("Simulação concluída, comando NÃO executado.")
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
				blocks = newBlocks // troca para os novos comandos da IA!
				break              // Sai desse loop for & reinicia com novos comandos (outputs será resetado)
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
			fmt.Print(outStr)
		}
	}
	fmt.Println("\n✅ Processamento de comandos concluído.")
}

// executeCommandsWithOutput executa todos os comandos do bloco (1 a 1), imprime e retorna o output total e último erro.
func (a *AgentMode) executeCommandsWithOutput(ctx context.Context, block CommandBlock) (string, string) {
	var allOutput strings.Builder
	var lastError string
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
		// Checagem de comando perigoso
		if isDangerous(cmd) {
			confirm := askLine("Se tem certeza que deseja executar, digite exatamente: 'sim, quero executar conscientemente'\nConfirma? ")
			if confirm != "sim, quero executar conscientemente" {
				outText := "Execução do comando perigoso ABORTADA pelo agente.\n"
				allOutput.WriteString(outText)
				fmt.Print(outText)
				continue
			}
		}

		header := fmt.Sprintf("⚙️ Comando %d/%d: %s\n", i+1, len(block.Commands), cmd)
		allOutput.WriteString(header)
		fmt.Print(header)

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

	finalMsg := "---------------------------------------\nExecução concluída.\n"
	allOutput.WriteString(finalMsg)
	fmt.Print(finalMsg)
	return allOutput.String(), lastError
}

// simulateCommandBlock tenta rodar os comandos de um bloco em modo "simulado"
// Para shell: adiciona "echo" antes, para docker/k8s/git: usa flag conhecida se suportado (rústico)
// Não Garante 100% (uns comandos não suportam dry-run), mas serve para preview
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
		simCmd := fmt.Sprintf("echo '[dry-run] Vai executar: %s'; %s", cmd, cmd)
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
