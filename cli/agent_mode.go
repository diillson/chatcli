/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/diillson/chatcli/llm/openai_assistant"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// AgentMode representa a funcionalidade de agente autônomo no ChatCLI
type AgentMode struct {
	cli                 *ChatCLI
	logger              *zap.Logger
	executeCommandsFunc func(ctx context.Context, block CommandBlock) (string, string)
}

// CommandContextInfo contém metadados sobre a origem e natureza de um comando
type CommandContextInfo struct {
	SourceType    SourceType
	FileExtension string
	IsScript      bool
	ScriptType    string // shell, python, etc.
}

type SourceType int

// CommandBlock representa um bloco de comandos executáveis
type CommandBlock struct {
	Description string
	Commands    []string
	Language    string
	ContextInfo CommandContextInfo
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

const (
	SourceTypeUserInput SourceType = iota
	SourceTypeFile
	SourceTypeCommandOutput
)

// NewAgentMode cria uma nova instância do modo agente
func NewAgentMode(cli *ChatCLI, logger *zap.Logger) *AgentMode {
	a := &AgentMode{
		cli:    cli,
		logger: logger,
	}
	a.executeCommandsFunc = a.executeCommandsWithOutput
	return a
}

// getInput obtém entrada do usuário de forma segura
func (a *AgentMode) getInput(prompt string) string {
	// Adicionado: Restaura o terminal para o modo 'sane' antes de ler a entrada
	cmd := exec.Command("stty", "sane")
	cmd.Stdin = os.Stdin // Garante que o comando opere no terminal correto
	_ = cmd.Run()        // Ignoramos erros, pois 'stty' pode não existir no Windows

	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		if err == io.EOF { // Ctrl+D
			return "q" // Trata Ctrl+D como um comando para sair do agente
		}
		a.logger.Warn("Erro ao ler entrada no modo agente", zap.Error(err))
		return ""
	}
	return strings.TrimSpace(input)
}

var (
	extraDangerPatterns []*regexp.Regexp
	allowSudo           bool
)

func init() {
	if s := os.Getenv("CHATCLI_AGENT_DENYLIST"); s != "" {
		for _, pat := range strings.Split(s, ";") {
			pat = strings.TrimSpace(pat)
			if pat == "" {
				continue
			}
			if r, err := regexp.Compile(pat); err == nil {
				extraDangerPatterns = append(extraDangerPatterns, r)
			}
		}
	}
	allowSudo = strings.EqualFold(os.Getenv("CHATCLI_AGENT_ALLOW_SUDO"), "true")
}

func isDangerous(cmd string) bool {
	// regras existentes
	for _, pattern := range dangerousPatterns {
		if regexp.MustCompile(pattern).MatchString(cmd) {
			return true
		}
	}
	// denylist extra
	for _, r := range extraDangerPatterns {
		if r.MatchString(cmd) {
			return true
		}
	}
	// sudo opcionalmente proibido
	if !allowSudo && regexp.MustCompile(`(?i)\bsudo\b`).MatchString(cmd) {
		return true
	}
	return false
}

// Run inicia o modo agente com uma pergunta do usuário
func (a *AgentMode) Run(ctx context.Context, query string, additionalContext string) error {
	_, isAssistant := a.cli.Client.(*openai_assistant.OpenAIAssistantClient)

	if isAssistant {
		a.logger.Debug("Executando modo agente com OpenAI Assistant")
	}

	var systemInstruction string
	if isAssistant {
		// Versão resumida para Assistants
		systemInstruction = "Você é um assistente de linha de comando que ajuda o usuário a executar tarefas no sistema de forma segura. " +
			"Sempre explique brevemente o propósito antes dos comandos. Prefira comandos simples e não interativos. " +
			"Evite comandos potencialmente destrutivos (rm -rf, dd, mkfs, etc.) sem um aviso claro de risco e alternativas seguras. " +
			"Quando sugerir comandos executáveis, use blocos de código exatamente no formato:\n\n" +
			"```execute:<tipo>\n<comandos>\n```\n\n" +
			"Tipos aceitos: shell, git, docker, kubectl. Se houver ambiguidade, faça uma pergunta antes de fornecer comandos."
	} else {
		// obter contexto do sistema
		osName := runtime.GOOS
		shellName := utils.GetUserShell()
		currentDir, err := os.Getwd()
		if err != nil {
			a.logger.Warn("Não foi possível obter diretório atual", zap.Error(err))
			currentDir = "desconhecido"
		}

		// Template sem crases/backticks brutos (para evitar fechamento prematuro do raw string)
		systemInstructionTemplate := `Você é um assistente especialista em linha de comando, operando dentro de um terminal. Seu objetivo é ajudar o usuário a realizar tarefas de forma segura e eficiente, fornecendo os comandos corretos.
    
    **[Contexto Disponível]**
    - Sistema Operacional: %s
    - Shell Padrão: %s
    - Diretório Atual: %s
    
    **[PROCESSO OBRIGATÓRIO]**
    Para cada solicitação do usuário, você DEVE seguir estritamente estas duas etapas:
    
    **Etapa 1: Planejamento**
    Pense passo a passo de forma interna. Se necessário, resuma o raciocínio em uma tag <reasoning> para mostrar ao usuário.
    
    **Etapa 2: Resposta Final Estruturada**
    Após o raciocínio, forneça a resposta final contendo:
    1. Uma tag <explanation> com uma explicação clara e concisa do que os comandos farão.
    2. Um ou mais blocos de código no formato de exemplo (o bloco de exemplo real é injetado abaixo).
    
    **[DIRETRIZES E RESTRIÇÕES]**
    1. Segurança é Prioridade: NUNCA sugira comandos destrutivos ('rm -rf', 'dd', 'mkfs', etc.) sem um aviso explícito sobre os riscos na tag <explanation>.
    2. Clareza: Prefira comandos que sejam fáceis de entender. Se um comando for complexo (ex: 'awk', 'sed'), explique cada parte dele.
    3. Eficiência: Use pipes ('|') e combine comandos para criar soluções eficientes quando apropriado.
    4. Interatividade: Evite comandos interativos (ex: 'vim', 'nano', 'ssh' sem argumentos). Se for necessário, avise o usuário na <explanation> e adicione o marcador #interactive ao final do comando (ex.: 'ssh user@host #interactive') para que a CLI trate como interativo.
    5. Ambiguidade: Se o pedido do usuário for ambíguo, em vez de adivinhar, faça uma pergunta para esclarecer. NÃO forneça um bloco execute nesse caso.
    6. Formato: Use blocos de código do tipo execute:<tipo> conforme exemplo injetado abaixo.
    
    **[EXEMPLO COMPLETO]**
    
    **Solicitação do Usuário:** "liste todos os arquivos go neste projeto e conte as linhas de cada um"
    
    **Sua Resposta:**
    <reasoning>
    1. O usuário quer encontrar todos os arquivos com a extensão .go. O comando 'find' é ideal para isso.
    2. O ponto de partida da busca deve ser o diretório atual ('.').
    3. O critério de busca é o nome do arquivo, então usarei: find . -name "*.go"
    4. Para cada arquivo encontrado, o usuário quer contar as linhas. O comando 'wc -l' faz isso.
    5. Preciso combinar find com wc -l. A melhor forma de fazer isso para múltiplos arquivos é usando xargs ou a opção -exec do find. A opção -exec com + é eficiente.
    6. O comando final será: find . -name "*.go" -exec wc -l {} +
    </reasoning>
    <explanation>
    Vou usar o comando 'find' para procurar recursivamente por todos os arquivos que terminam com .go a partir do diretório atual. Em seguida, para cada arquivo encontrado, vou executar o comando 'wc -l' para contar o número de linhas.
    </explanation>
    
    Exemplo de bloco de comando (formato mostrado abaixo):`

		// bloco de exemplo real (aqui incluímos as crases)
		codeFence := "```execute:shell\nfind . -name \"*.go\" -exec wc -l {} +\n```"

		// placeholders (osName, shellName, currentDir) + injetar codeFence como último %s
		systemInstruction = fmt.Sprintf(systemInstructionTemplate, osName, shellName, currentDir) + "\n\n" + codeFence
	}

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
	a.cli.animation.ShowThinkingAnimation(a.cli.Client.GetModelName())

	// 5. Enviar para a LLM e obter a resposta
	var responseCtx context.Context
	var cancel context.CancelFunc

	if isAssistant {
		a.logger.Debug("Usando timeout estendido para OpenAI Assistant")
		responseCtx, cancel = context.WithTimeout(ctx, 30*time.Minute)
	} else {
		responseCtx, cancel = context.WithTimeout(ctx, 30*time.Minute)
	}
	defer cancel()

	a.logger.Debug("Enviando prompt para o LLM",
		zap.String("provider", a.cli.Provider),
		zap.Int("historyLength", len(a.cli.history)),
		zap.Int("queryLength", len(fullQuery)))

	aiResponse, err := a.cli.Client.SendPrompt(responseCtx, fullQuery, a.cli.history, 0)

	if err != nil {
		a.logger.Error("Erro ao obter resposta do LLM", zap.Error(err))
	} else {
		a.logger.Debug("Resposta recebida do LLM",
			zap.Int("responseLength", len(aiResponse)))
	}

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
		a.handleCommandBlocks(context.Background(), commandBlocks)
	} else {
		fmt.Println("\nNenhum comando executável encontrado na resposta.")
	}
	return nil
}

func (a *AgentMode) RunOnce(ctx context.Context, query string, autoExecute bool) error {
	// 1. Preparar a requisição para a LLM com um prompt OTIMIZADO para one-shot.
	systemInstruction := `Você é um assistente de linha de comando operando em um modo de execução única (one-shot).
                Sua tarefa é analisar o pedido do usuário e fornecer **um único e conciso bloco de comando** que resolva a tarefa da forma mais eficiente e segura possível.
    
    - Responda **apenas** com o melhor bloco de comando no formato ` + "```" + `execute:shell.
	- **Não** forneça múltiplos blocos de comando ou alternativas.
	- **Não** adicione explicações longas antes ou depois, apenas o comando necessário para a execução.
	- Evite comandos destrutivos (como rm -rf) a menos que seja explicitamente solicitado e a intenção seja clara.
	- O comando deve ser diretamente executável dado que precisamos apenas de um unico comando o melhor e expert possivel.`

	a.cli.history = append(a.cli.history, models.Message{Role: "system", Content: systemInstruction})
	a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: query})

	a.cli.animation.ShowThinkingAnimation(a.cli.Client.GetModelName())

	// 2. Enviar para a LLM
	aiResponse, err := a.cli.Client.SendPrompt(ctx, query, a.cli.history, 0)
	if err != nil {
		return fmt.Errorf("erro ao obter resposta da IA: %w", err)
	}
	a.cli.animation.StopThinkingAnimation()
	if err != nil {
		return fmt.Errorf("erro ao obter resposta da IA: %w", err)
	}

	// 3. Extrair blocos de comando
	commandBlocks := a.extractCommandBlocks(aiResponse)

	// A IA pode, ocasionalmente, adicionar uma breve explicação. Vamos mostrá-la.
	a.displayResponseWithoutCommands(aiResponse, commandBlocks)

	if len(commandBlocks) == 0 {
		fmt.Println("\nNenhum comando executável foi sugerido pela IA.")
		return nil
	}

	// 4. Lógica de execução ou "dry-run"
	if !autoExecute {
		// MODO DRY-RUN (PADRÃO)
		fmt.Println("\n🤖 MODO AGENTE (ONE-SHOT): Comando Sugerido")
		fmt.Println("==============================================")
		fmt.Println("Para executar automaticamente, use o flag --agent-auto-exec")

		// Como esperamos apenas um bloco, a lógica fica mais simples
		block := commandBlocks[0]
		fmt.Printf("\n🔷 Bloco de Comando: %s\n", block.Description)
		fmt.Printf("  Linguagem: %s\n", block.Language)
		for _, cmd := range block.Commands {
			fmt.Printf("    $ %s\n", cmd)
		}

		return nil
	}

	// MODO AUTO-EXECUTE
	fmt.Println("\n🤖 MODO AGENTE (ONE-SHOT): Execução Automática")
	fmt.Println("===============================================")

	blockToExecute := commandBlocks[0]

	// VERIFICAÇÃO DE SEGURANÇA CRÍTICA
	for _, cmd := range blockToExecute.Commands {
		if isDangerous(cmd) {
			errMsg := fmt.Sprintf("execução automática abortada por segurança. O comando sugerido é potencialmente perigoso: %q", cmd)
			fmt.Printf("⚠️ %s\n", errMsg)
			return errors.New(errMsg)
		}
	}

	fmt.Printf("✅ Comando seguro detectado. Executando o comando sugerido...\n")
	_, errorMsg := a.executeCommandsWithOutput(ctx, blockToExecute)

	if errorMsg != "" {
		return fmt.Errorf("o comando foi executado, mas retornou um erro: %s", errorMsg)
	}

	return nil
}

// findLastMeaningfulLine extrai a última linha não vazia de um bloco de texto.
func findLastMeaningfulLine(text string) string {
	lines := strings.Split(text, "\n")
	// Itera de trás para frente para encontrar a última linha relevante
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		// Ignora linhas vazias ou que são parte de outro bloco de código
		if line != "" && !strings.HasPrefix(line, "```") {
			return line
		}
	}
	return "" // Retorna vazio se nenhuma descrição for encontrada
}

// extractCommandBlocks extrai blocos de comando da resposta da IA de forma mais robusta.
func (a *AgentMode) extractCommandBlocks(response string) []CommandBlock {
	var commandBlocks []CommandBlock

	_, isAssistant := a.cli.Client.(*openai_assistant.OpenAIAssistantClient)
	if isAssistant {
		return a.extractCommandBlocksForAssistant(response)
	}

	re := regexp.MustCompile("(?s)```execute:\\s*([a-zA-Z0-9_-]+)\\s*\n(.*?)```")

	// 1. Encontrar todos os blocos de comando
	matches := re.FindAllStringSubmatch(response, -1)
	if len(matches) == 0 {
		// fallback para blocos de shell puros
		fb := regexp.MustCompile("(?s)```(?:sh|bash|shell)\\s*\\n(.*?)```").FindAllStringSubmatch(response, -1)
		for _, m := range fb {
			commandsStr := strings.TrimSpace(m[1])
			commandBlocks = append(commandBlocks, CommandBlock{
				Description: "Comandos extraídos de bloco shell",
				Commands:    splitCommandsByBlankLine(commandsStr),
				Language:    "shell",
				ContextInfo: CommandContextInfo{SourceType: SourceTypeUserInput, IsScript: isShellScript(commandsStr), ScriptType: "shell"},
			})
		}
		return commandBlocks
	}

	// 2. Dividir a resposta usando os blocos como delimitadores.
	parts := re.Split(response, -1)

	// 3. Iterar sobre os blocos encontrados e associar a descrição correta
	for i, match := range matches {
		if len(match) >= 3 {
			language := strings.TrimSpace(match[1])
			commandsStr := strings.TrimSpace(match[2])

			var description string
			if i < len(parts) {
				// 1. Tenta extrair a descrição da tag <explanation> (mais robusto)
				explanationRe := regexp.MustCompile("(?s)<explanation>(.*?)</explanation>")
				explanationMatch := explanationRe.FindStringSubmatch(parts[i])

				if len(explanationMatch) > 1 {
					description = strings.TrimSpace(explanationMatch[1])
				} else {
					// 2. Se não encontrar a tag, usa o método antigo como fallback
					description = findLastMeaningfulLine(parts[i])
				}
			}

			isScript := false
			if strings.Contains(commandsStr, "<<") ||
				strings.Contains(commandsStr, "cat >") ||
				regexp.MustCompile(`if\s+.*\s+then`).MatchString(commandsStr) ||
				regexp.MustCompile(`for\s+.*\s+do`).MatchString(commandsStr) {
				isScript = true
			}

			var commandsList []string
			if isScript {
				commandsList = []string{commandsStr}
			} else {
				commandsList = splitCommandsByBlankLine(commandsStr)
			}

			commandBlocks = append(commandBlocks, CommandBlock{
				Description: description,
				Commands:    commandsList,
				Language:    language,
				ContextInfo: CommandContextInfo{
					SourceType: SourceTypeUserInput,
					IsScript:   isScript,
					ScriptType: language,
				},
			})
		}
	}

	return commandBlocks
}

// Função para extrair comandos de respostas do OpenAI Assistant
func (a *AgentMode) extractCommandBlocksForAssistant(response string) []CommandBlock {
	var commandBlocks []CommandBlock

	// Padrões de extração mais flexíveis para o assistente
	// 1. Blocos de código padrão
	codeBlockRe := regexp.MustCompile("```(?:sh|bash|shell)?\\s*\n([\\s\\S]*?)```")
	codeMatches := codeBlockRe.FindAllStringSubmatch(response, -1)

	// 2. Linhas que parecem comandos shell (começam com $ ou #)
	commandLineRe := regexp.MustCompile(`(?m)^[$#]\s*(.+)$`)
	commandMatches := commandLineRe.FindAllStringSubmatch(response, -1)

	// Processar blocos de código
	for _, match := range codeMatches {
		if len(match) >= 2 {
			commands := splitCommandsByBlankLine(match[1])

			// Buscar descrição antes do bloco
			description := findDescriptionBeforeBlock(response, match[0])

			commandBlocks = append(commandBlocks, CommandBlock{
				Description: description,
				Commands:    commands,
				Language:    "shell", // Assumir shell como padrão
				ContextInfo: CommandContextInfo{
					SourceType: SourceTypeUserInput,
					IsScript:   len(commands) > 1 || isShellScript(match[1]),
					ScriptType: "shell",
				},
			})
		}
	}

	// Se não encontrar blocos de código, tentar linhas de comando
	if len(commandBlocks) == 0 && len(commandMatches) > 0 {
		var commands []string
		for _, match := range commandMatches {
			if len(match) >= 2 {
				cmd := strings.TrimSpace(match[1])
				if cmd != "" {
					commands = append(commands, cmd)
				}
			}
		}

		if len(commands) > 0 {
			commandBlocks = append(commandBlocks, CommandBlock{
				Description: "Comandos extraídos da resposta",
				Commands:    commands,
				Language:    "shell",
				ContextInfo: CommandContextInfo{
					SourceType: SourceTypeUserInput,
					IsScript:   false,
				},
			})
		}
	}

	return commandBlocks
}

// Função auxiliar para encontrar uma descrição antes de um bloco de código
func findDescriptionBeforeBlock(response, block string) string {
	blockIndex := strings.Index(response, block)
	if blockIndex <= 0 {
		return ""
	}

	// Obter até 200 caracteres antes do bloco
	startIndex := max(0, blockIndex-200)
	prefix := response[startIndex:blockIndex]

	// Dividir em linhas e pegar a última linha não vazia
	lines := strings.Split(prefix, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" {
			return line
		}
	}

	return ""
}

func splitCommandsByBlankLine(src string) []string {
	var cmds []string
	var buf []string
	lines := strings.Split(src, "\n")
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			if len(buf) > 0 {
				cmds = append(cmds, strings.Join(buf, "\n"))
				buf = nil
			}
		} else {
			buf = append(buf, l)
		}
	}
	if len(buf) > 0 {
		cmds = append(cmds, strings.Join(buf, "\n"))
	}
	return cmds
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
	a.cli.typewriterEffect(fmt.Sprintf("\n%s:\n%s\n", a.cli.Client.GetModelName(), renderedResponse), 2*time.Millisecond)
}

// getMultilineInput obtém entrada de múltiplas linhas do usuário
// Suporta:
// - ENTER vazio na primeira linha para continuar sem contexto
// - "." sozinho em uma linha para finalizar a entrada
// - Control+D (EOF) para finalizar a entrada
func (a *AgentMode) getMultilineInput(prompt string) string {
	fmt.Print(prompt)
	fmt.Println("(Digite '.' sozinho em uma linha para finalizar ou Ctrl+D)")

	// LÓGICA DO LINER REMOVIDA
	var lines []string
	reader := bufio.NewReader(os.Stdin)

	for {
		line, err := reader.ReadString('\n')
		if err != nil { // Trata EOF (Ctrl+D)
			break
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "." {
			break
		}
		lines = append(lines, line)
	}

	return strings.Join(lines, "\n")
}

// requestLLMContinuationWithContext reenvia o contexto/output + contexto adicional do usuário para a LLM
func (a *AgentMode) requestLLMContinuationWithContext(ctx context.Context, previousCommand, output, stderr, userContext string) ([]CommandBlock, error) {
	// Criar um novo contexto com timeout para esta operação específica
	newCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var prompt strings.Builder

	prompt.WriteString("O comando sugerido anteriormente foi:\n")
	prompt.WriteString(previousCommand)
	outSafe := utils.SanitizeSensitiveText(output)
	errSafe := utils.SanitizeSensitiveText(stderr)

	prompt.WriteString("\n\nO resultado (stdout) foi:\n")
	prompt.WriteString(outSafe)

	if errSafe != "" {
		prompt.WriteString("\n\nO erro (stderr) foi:\n")
		prompt.WriteString(errSafe)
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

	a.cli.animation.ShowThinkingAnimation(a.cli.Client.GetModelName())
	aiResponse, err := a.cli.Client.SendPrompt(newCtx, prompt.String(), a.cli.history, 0)
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
	outputs := make([]*CommandOutput, len(blocks))

mainLoop:
	for {
		// --- NOVO CABEÇALHO ---
		fmt.Println("\n" + colorize(" "+strings.Repeat("━", 58), ColorGray))
		fmt.Println(colorize(" 🤖 MODO AGENTE: PLANO DE AÇÃO", ColorLime+ColorBold))
		fmt.Println(colorize(" "+strings.Repeat("━", 58), ColorGray))
		fmt.Println(colorize(" A IA sugeriu os seguintes comandos para executar sua tarefa.", ColorGray))

		// --- NOVOS CARTÕES DE COMANDO ---
		for i, block := range blocks {
			description := block.Description
			if description == "" {
				description = "Executar comandos"
			}
			fmt.Printf("\n"+colorize(" 🔷 COMANDO #%d: %s", ColorPurple+ColorBold), i+1, description)
			fmt.Printf("\n"+colorize("    Tipo: %s", ColorGray), block.Language)
			fmt.Println(colorize("\n    Código:", ColorGray))
			for _, cmd := range block.Commands {
				// Adiciona um prefixo '$' para comandos shell para clareza
				prefix := ""
				if block.Language == "shell" {
					prefix = "$ "
				}
				// Imprime cada linha do comando com indentação
				for _, line := range strings.Split(cmd, "\n") {
					fmt.Printf(colorize("      %s%s\n", ColorCyan), prefix, line)
				}
			}
		}

		// --- NOVO MENU DE OPÇÕES (COMPLETO E CORRIGIDO) ---
		fmt.Println("\n" + colorize(strings.Repeat("-", 60), ColorGray))
		fmt.Println(colorize(" O QUE VOCÊ DESEJA FAZER?", ColorLime+ColorBold))
		fmt.Println(colorize(strings.Repeat("-", 60), ColorGray))

		// Usamos fmt.Sprintf para alinhar as descrições perfeitamente
		fmt.Printf("  %s: Executa um comando específico (ex: 1, 2, ...)\n", colorize(fmt.Sprintf("%-6s", "[1..N]"), ColorYellow))
		fmt.Printf("  %s: Executa todos os comandos em sequência\n", colorize(fmt.Sprintf("%-6s", "a"), ColorYellow))
		fmt.Printf("  %s: Edita o comando N antes de executar (ex: e1)\n", colorize(fmt.Sprintf("%-6s", "eN"), ColorYellow))
		fmt.Printf("  %s: Simula (dry-run) o comando N (ex: t2)\n", colorize(fmt.Sprintf("%-6s", "tN"), ColorYellow))                   // <<< ADICIONADO
		fmt.Printf("  %s: Pede continuação à IA com a saída do comando N (ex: c2)\n", colorize(fmt.Sprintf("%-6s", "cN"), ColorYellow)) // <<< ADICIONADO
		fmt.Printf("  %s: Adiciona contexto ao comando N ANTES de executar (ex: pc1)\n", colorize(fmt.Sprintf("%-6s", "pcN"), ColorYellow))
		fmt.Printf("  %s: Adiciona contexto à SAÍDA do comando N (ex: ac1)\n", colorize(fmt.Sprintf("%-6s", "acN"), ColorYellow))
		fmt.Printf("  %s: Sai do Modo Agente\n", colorize(fmt.Sprintf("%-6s", "q"), ColorYellow))

		fmt.Println(colorize(strings.Repeat("-", 60), ColorGray))

		// --- PROMPT DE ENTRADA ESTILIZADO ---
		prompt := colorize("\n ➤ Sua escolha: ", ColorLime)
		answer := a.getInput(prompt)
		answer = strings.ToLower(strings.TrimSpace(answer))

		switch {
		case answer == "q":
			fmt.Println(colorize("\n ✅ Saindo do modo agente.", ColorGray))
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
			_ = cmd.Run() // Ignoramos erros aqui propositalmente

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

				freshCtx, freshCancel := a.refreshContext()

				outStr, errStr := a.executeCommandsFunc(freshCtx, block)

				freshCancel()

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
			fmt.Println("\nResumo:")
			for i, out := range outputs {
				status := "OK"
				if out == nil || strings.TrimSpace(out.ErrorMsg) != "" {
					status = "ERRO"
				}
				fmt.Printf("- #%d: %s\n", i+1, status)
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

			freshCtx, freshCancel := a.refreshContext()

			editedBlock := blocks[cmdNum-1]
			editedBlock.Commands = edited

			outStr, errStr := a.executeCommandsFunc(freshCtx, editedBlock)

			freshCancel()

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
				freshCtx, freshCancel := a.refreshContext()

				outStr, errStr := a.executeCommandsFunc(freshCtx, blocks[cmdNum-1])

				freshCancel()

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

				freshCtx, freshCancel := a.refreshContext()

				// Chamar método para tratar a continuação sem contexto adicional
				newBlocks, err := a.requestLLMContinuationWithContext(
					freshCtx,
					strings.Join(blocks[cmdNum-1].Commands, "\n"),
					outputs[cmdNum-1].Output,
					outputs[cmdNum-1].ErrorMsg,
					"", // Contexto vazio
				)
				freshCancel()
				if err != nil {
					fmt.Println("Erro ao pedir continuação à IA:", err)
					continue
				}
				if len(newBlocks) > 0 {
					blocks = newBlocks                            // troca para os novos comandos da IA!
					outputs = make([]*CommandOutput, len(blocks)) // Reset outputs para o novo tamanho
					continue mainLoop                             // Sai desse loop for & reinicia com novos comandos
				} else {
					fmt.Println("\nNenhum comando sugerido pela IA na resposta.")
				}
			} else {
				fmt.Println("\nContexto recebido! Enviando para a IA...")

				freshCtx, freshCancel := a.refreshContext()

				// Chamar método para tratar a continuação com contexto
				newBlocks, err := a.requestLLMContinuationWithContext(
					freshCtx,
					strings.Join(blocks[cmdNum-1].Commands, "\n"),
					outputs[cmdNum-1].Output,
					outputs[cmdNum-1].ErrorMsg,
					userContext,
				)
				freshCancel()
				if err != nil {
					fmt.Println("Erro ao pedir continuação à IA:", err)
					continue
				}
				if len(newBlocks) > 0 {
					blocks = newBlocks                            // troca para os novos comandos da IA!
					outputs = make([]*CommandOutput, len(blocks)) // Reset outputs para o novo tamanho
					continue mainLoop                             // Sai desse loop for & reinicia com novos comandos
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

			freshCtx, freshCancel := a.refreshContext()

			newBlocks, err := a.requestLLMContinuation(
				freshCtx,
				strings.Join(blocks[cmdNum-1].Commands, "\n"),
				strings.Join(blocks[cmdNum-1].Commands, "\n"),
				outputs[cmdNum-1].Output,
				outputs[cmdNum-1].ErrorMsg,
			)
			freshCancel()
			if err != nil {
				fmt.Println("Erro ao pedir continuação à IA:", err)
				continue
			}
			if len(newBlocks) > 0 {
				blocks = newBlocks                            // troca para os novos comandos da IA!
				outputs = make([]*CommandOutput, len(blocks)) // Reset outputs para o novo tamanho
				continue mainLoop                             // Sai desse loop for & reinicia com novos comandos
			} else {
				fmt.Println("\nNenhum comando sugerido pela IA na resposta.")
			}

		case strings.HasPrefix(answer, "pc"):
			cmdNumStr := strings.TrimPrefix(answer, "pc")
			cmdNum, err := strconv.Atoi(cmdNumStr)
			if err != nil || cmdNum < 1 || cmdNum > len(blocks) {
				fmt.Println("Número inválido para adicionar pré-contexto.")
				continue
			}

			// Obter o contexto do usuário
			userContext := a.getMultilineInput("Digite seu contexto ou instrução adicional para o comando:\n")
			if userContext == "" {
				fmt.Println("Nenhum contexto fornecido. Operação cancelada.")
				continue
			}

			fmt.Println("\nContexto recebido! Solicitando refinamento do comando à IA...")

			// Chamar a nova função para obter comandos refinados
			newBlocks, err := a.requestLLMWithPreExecutionContext(
				ctx,
				strings.Join(blocks[cmdNum-1].Commands, "\n"),
				userContext,
			)
			if err != nil {
				fmt.Println("Erro ao solicitar refinamento à IA:", err)
				continue
			}
			if len(newBlocks) > 0 {
				blocks = newBlocks                            // Substitui os comandos antigos pelos novos
				outputs = make([]*CommandOutput, len(blocks)) // Reseta os outputs
				continue mainLoop                             // Reinicia o loop com os novos comandos
			} else {
				fmt.Println("\nA IA não sugeriu novos comandos. Mantendo os comandos atuais.")
			}

		default:
			cmdNum, err := strconv.Atoi(answer)
			if err != nil || cmdNum < 1 || cmdNum > len(blocks) {
				fmt.Println("Opção inválida.")
				continue
			}

			execCtx, execCancel := a.refreshContext()

			outStr, errStr := a.executeCommandsFunc(execCtx, blocks[cmdNum-1])

			execCancel()

			outputs[cmdNum-1] = &CommandOutput{
				CommandBlock: blocks[cmdNum-1],
				Output:       outStr,
				ErrorMsg:     errStr,
			}
		}
	}
}

func (a *AgentMode) refreshContext() (context.Context, context.CancelFunc) {
	toStr := utils.GetEnvOrDefault("CHATCLI_AGENT_CMD_TIMEOUT", "10m")
	d, err := time.ParseDuration(toStr)
	if err != nil || d <= 0 {
		d = 10 * time.Minute
	}
	return context.WithTimeout(context.Background(), d)
}

// executeCommandsWithOutput executa todos os comandos do bloco com uma UI dinâmica, segura e alinhada.
func (a *AgentMode) executeCommandsWithOutput(ctx context.Context, block CommandBlock) (string, string) {
	var allOutput strings.Builder // Para enviar à IA (sanitizado)
	var lastError string

	// Normaliza linguagem para fins de exibição/decisão
	langNorm := strings.ToLower(block.Language)
	if langNorm == "git" || langNorm == "docker" || langNorm == "kubectl" {
		langNorm = "shell"
	}

	// --- CABEÇALHO DINÂMICO ---
	titleContent := fmt.Sprintf(" 🚀 EXECUTANDO: %s", langNorm)
	contentWidth := visibleLen(titleContent)
	topBorder := strings.Repeat("─", contentWidth)
	fmt.Println("\n" + colorize(topBorder, ColorGray))
	fmt.Println(colorize(titleContent, ColorLime+ColorBold))

	// Adiciona uma versão simples ao log para a IA
	allOutput.WriteString(fmt.Sprintf("\nExecutando: %s (tipo: %s)\n", block.Description, langNorm))

	// Descobre shell
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}

	if block.ContextInfo.IsScript {
		// --- EXECUÇÃO DE SCRIPT COMPLETO (UM BLOCO) ---
		scriptContent := block.Commands[0]
		tmpFile, err := os.CreateTemp("", "chatcli-script-*.sh")
		if err != nil {
			errMsg := fmt.Sprintf("❌ Erro ao criar arquivo temporário para script: %v\n", err)
			fmt.Print(errMsg)
			allOutput.WriteString(errMsg)
			lastError = err.Error()
		} else {
			scriptPath := tmpFile.Name()
			defer func() { _ = os.Remove(scriptPath) }()

			if _, werr := tmpFile.WriteString(scriptContent); werr != nil {
				errMsg := fmt.Sprintf("❌ Erro ao escrever arquivo temporário de script: %v\n", werr)
				fmt.Print(errMsg)
				allOutput.WriteString(errMsg)
				lastError = werr.Error()
			}
			_ = tmpFile.Close()
			_ = os.Chmod(scriptPath, 0755)

			header := fmt.Sprintf("⚙️ Executando script via %s:\n", shell)
			fmt.Print(header)
			allOutput.WriteString(header)

			start := time.Now()
			cmd := exec.CommandContext(ctx, shell, scriptPath)
			output, err := cmd.CombinedOutput()
			duration := time.Since(start)

			// Sanitiza antes de imprimir/salvar
			safe := utils.SanitizeSensitiveText(string(output))
			for _, line := range strings.Split(strings.TrimRight(safe, "\n"), "\n") {
				fmt.Println("  " + line)
			}
			allOutput.WriteString(safe + "\n")

			// Exit code (quando disponível)
			exitCode := 0
			if err != nil {
				if ee, ok := err.(*exec.ExitError); ok {
					if ws, ok := ee.Sys().(syscall.WaitStatus); ok {
						exitCode = ws.ExitStatus()
					}
				}
				errMsg := fmt.Sprintf("❌ Erro: %v\n", err)
				allOutput.WriteString(errMsg)
				lastError = err.Error()
			}
			// Metadados
			meta := fmt.Sprintf("  [exit=%d, duração=%s]\n", exitCode, duration)
			fmt.Print(meta)
			allOutput.WriteString(fmt.Sprintf("[meta] exit=%d duration=%s\n", exitCode, duration))
		}
	} else {
		// --- EXECUÇÃO DE COMANDOS INDIVIDUAIS ---
		for i, cmd := range block.Commands {
			if cmd == "" {
				continue
			}

			trimmed := strings.TrimSpace(cmd)

			// Suporte nativo a "cd"
			if strings.HasPrefix(trimmed, "cd ") || trimmed == "cd" {
				target := strings.TrimSpace(strings.TrimPrefix(trimmed, "cd"))
				if target == "" {
					target = "~"
				}
				// Expansão simples de ~
				if strings.HasPrefix(target, "~") {
					if home, err := os.UserHomeDir(); err == nil {
						if target == "~" {
							target = home
						} else if strings.HasPrefix(target, "~/") {
							target = filepath.Join(home, target[2:])
						}
					}
				}
				if err := os.Chdir(target); err != nil {
					msg := fmt.Sprintf("❌ Erro ao trocar diretório para '%s': %v\n", target, err)
					fmt.Print(msg)
					allOutput.WriteString(msg)
					lastError = err.Error()
				} else {
					wd, _ := os.Getwd()
					msg := fmt.Sprintf("📂 Diretório alterado para: %s\n", wd)
					fmt.Print(msg)
					allOutput.WriteString(msg)
				}
				// Continua para o próximo comando do bloco
				continue
			}

			// Segurança: confirmação para comandos perigosos
			if isDangerous(trimmed) {
				confirmPrompt := "Este comando é potencialmente perigoso. Para confirmar, digite: 'sim, quero executar conscientemente'\nConfirma?: "
				confirm := a.getCriticalInput(confirmPrompt)
				if confirm != "sim, quero executar conscientemente" {
					outText := "Execução do comando perigoso ABORTADA.\n"
					fmt.Print(colorize(outText, ColorYellow))
					allOutput.WriteString(outText)
					continue
				}
				fmt.Println(colorize("⚠️ Confirmação recebida. Executando comando perigoso...", ColorYellow))
			}

			header := fmt.Sprintf("⚙️ Comando %d/%d: %s\n", i+1, len(block.Commands), trimmed)
			fmt.Print(header)
			allOutput.WriteString(header)

			// Heurística de interatividade
			isInteractive := false
			if strings.HasSuffix(trimmed, " --interactive") {
				trimmed = strings.TrimSuffix(trimmed, " --interactive")
				isInteractive = true
			} else if strings.Contains(trimmed, "#interactive") {
				trimmed = strings.ReplaceAll(trimmed, "#interactive", "")
				trimmed = strings.TrimSpace(trimmed)
				isInteractive = true
			} else {
				isInteractive = isLikelyInteractiveCommand(trimmed)
			}

			if !isInteractive && mightBeInteractive(trimmed, block.ContextInfo) {
				isInteractive = a.askUserIfInteractive(trimmed, block.ContextInfo)
			}

			// Execução
			if isInteractive {
				outText := "🖥️  Executando em modo interativo. O controle será passado para o comando.\n"
				fmt.Print(colorize(outText, ColorGray))
				allOutput.WriteString(outText)

				// Pausa para leitura
				time.Sleep(1 * time.Second)

				start := time.Now()
				err := a.executeInteractiveCommand(ctx, shell, trimmed)
				duration := time.Since(start)

				exitCode := 0
				if err != nil {
					if ee, ok := err.(*exec.ExitError); ok {
						if ws, ok := ee.Sys().(syscall.WaitStatus); ok {
							exitCode = ws.ExitStatus()
						}
					}
					errMsg := fmt.Sprintf("❌ Erro no comando interativo: %v\n", err)
					fmt.Print(errMsg)
					allOutput.WriteString(errMsg)
					lastError = err.Error()
				} else {
					okMsg := "✓ Comando interativo finalizado.\n"
					fmt.Print(okMsg)
					allOutput.WriteString(okMsg)
				}

				// Metadados
				meta := fmt.Sprintf("  [exit=%d, duração=%s]\n", exitCode, duration)
				fmt.Print(meta)
				allOutput.WriteString(fmt.Sprintf("[meta] exit=%d duration=%s\n", exitCode, duration))
			} else {
				// Não-interativo: capturar stdout+stderr
				start := time.Now()
				output, err := a.captureCommandOutput(ctx, shell, []string{"-c", trimmed})
				duration := time.Since(start)

				// Sanitiza antes de exibir/salvar
				safe := utils.SanitizeSensitiveText(string(output))
				for _, line := range strings.Split(strings.TrimRight(safe, "\n"), "\n") {
					fmt.Println("  " + line)
				}
				allOutput.WriteString(safe + "\n")

				exitCode := 0
				if err != nil {
					if ee, ok := err.(*exec.ExitError); ok {
						if ws, ok := ee.Sys().(syscall.WaitStatus); ok {
							exitCode = ws.ExitStatus()
						}
					}
					// O erro já está refletido no output (stderr foi anexado). Registra no buffer também.
					errMsg := fmt.Sprintf("❌ Erro: %v\n", err)
					allOutput.WriteString(errMsg)
					lastError = err.Error()
				}

				// Metadados
				meta := fmt.Sprintf("  [exit=%d, duração=%s]\n", exitCode, duration)
				fmt.Print(meta)
				allOutput.WriteString(fmt.Sprintf("[meta] exit=%d duration=%s\n", exitCode, duration))
			}
		}
	}

	// --- RODAPÉ DINÂMICO ---
	footerContent := " ✅ Execução Concluída "
	if lastError != "" {
		footerContent = " ⚠️ Execução Concluída com Erros "
	}
	footerWidth := visibleLen(footerContent)

	paddingWidth := contentWidth - footerWidth
	if paddingWidth < 0 {
		paddingWidth = 0
	}
	leftPadding := paddingWidth / 2
	rightPadding := paddingWidth - leftPadding

	finalBorder := strings.Repeat("─", leftPadding) + footerContent + strings.Repeat("─", rightPadding)
	fmt.Println(colorize(finalBorder, ColorGray))

	allOutput.WriteString("Execução concluída.\n")
	return allOutput.String(), lastError
}

// executeInteractiveCommand executa um comando interativo passando o controle do terminal
func (a *AgentMode) executeInteractiveCommand(ctx context.Context, shell string, command string) error {
	// Passo 1: Informar o usuário e restaurar o terminal para o modo normal.
	fmt.Println("\n--- Entrando no modo de comando interativo ---")
	fmt.Println("O controle do terminal será passado para o comando. Para retornar, saia do programa (ex: ':q' no vim, 'exit' no shell).")
	fmt.Println("----------------------------------------------")

	saneCmd := exec.Command("stty", "sane")
	saneCmd.Stdin = os.Stdin // Garante que o comando stty opere no terminal correto.
	if err := saneCmd.Run(); err != nil {
		a.logger.Warn("Falha ao restaurar o terminal para 'sane'. O comando interativo pode não se comportar como esperado.", zap.Error(err))
	}

	// Passo 2: Preparar e executar o comando do usuário.
	shellConfigPath := a.getShellConfigPath(shell)
	var shellCommand string
	if shellConfigPath != "" {
		// Constrói um comando que primeiro carrega o ambiente do shell e depois executa o comando do usuário.
		shellCommand = fmt.Sprintf("source %s 2>/dev/null || true; %s", shellConfigPath, command)
	} else {
		shellCommand = command
	}

	// Cria o comando a ser executado.
	cmd := exec.CommandContext(ctx, shell, "-c", shellCommand)

	// Passo 3: Conectar a entrada/saída/erro do comando diretamente ao terminal.
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()

	// Passo 4: O comando terminou. Informar o usuário e retornar.
	fmt.Println("\n--- Retornando ao ChatCLI ---")

	// Retorna o erro (se houver) da execução do comando.
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
		"terraform", "ansible", "git", "gitk", "git gui", "git rebase -i",
		"kubectl", "helm", "oc", "minikube", "vagrant", "packer",
		"terraform console", "gcloud", "aws", "az", "pulumi", "pulumi up",
		"npm", "yarn", "pnpm", "composer", "bundle", "cargo",
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

// detectHeredocs verifica a presença de heredocs em um script shell
func detectHeredocs(script string) bool {
	// Padrão para heredocs: <<EOF, <<'EOF', << EOF, <<-EOF etc.
	heredocPattern := regexp.MustCompile(`<<-?\s*['"]?(\w+)['"]?`)
	return heredocPattern.MatchString(script)
}

// isShellScript determina se o conteúdo é um script shell (e não apenas comandos individuais)
func isShellScript(content string) bool {
	// Verificar características específicas de scripts shell
	return detectHeredocs(content) ||
		strings.Contains(content, "#!/bin/") ||
		regexp.MustCompile(`if\s+.*\s+then`).MatchString(content) ||
		regexp.MustCompile(`for\s+.*\s+in\s+.*\s+do`).MatchString(content) ||
		regexp.MustCompile(`while\s+.*\s+do`).MatchString(content) ||
		regexp.MustCompile(`case\s+.*\s+in`).MatchString(content) ||
		strings.Contains(content, "function ") ||
		strings.Count(content, "{") > 1 && strings.Count(content, "}") > 1
}

// mightBeInteractive verifica se um comando pode ser interativo com lógica aprimorada
func mightBeInteractive(cmd string, contextInfo CommandContextInfo) bool {
	// Se o comando veio de um arquivo de log ou código, geralmente não é interativo
	if contextInfo.SourceType == SourceTypeFile {
		// Verificar extensões de arquivo de código/log
		if contextInfo.FileExtension != "" {
			nonInteractiveExtensions := map[string]bool{
				".log": true, ".js": true, ".ts": true, ".py": true, ".go": true,
				".java": true, ".php": true, ".rb": true, ".c": true, ".cpp": true,
			}
			if nonInteractiveExtensions[contextInfo.FileExtension] {
				return false
			}
		}

		// Se for conteúdo de arquivo, verificar características de código
		if hasCodeStructures(cmd) {
			return false
		}
	}

	// Lista de padrões que podem indicar interatividade em comandos shell
	possiblyInteractivePatterns := []string{
		"^ping\\s", "^traceroute\\s", "^nc\\s", "^netcat\\s", "^telnet\\s",
		"^ssh\\s", "^top$", "^htop$", "^vi\\s", "^vim\\s", "^nano\\s",
		"^less\\s", "^more\\s", "^tail -f", "^mysql\\s", "^psql\\s",
		"^docker exec -it", "^kubectl exec -it", "^python\\s+-i", "^node\\s+-i",
	}

	// Usar regex para verificar padrões de início de linha para comandos shell
	for _, pattern := range possiblyInteractivePatterns {
		matched, _ := regexp.MatchString(pattern, cmd)
		if matched {
			return true
		}
	}

	return false
}

// hasCodeStructures detecta estruturas comuns de código (como blocos try/catch, funções, etc.)
func hasCodeStructures(content string) bool {
	codePatterns := []string{
		// patterns de código comuns
		"try\\s*{", "catch\\s*\\(", "function\\s+\\w+\\s*\\(", "=>\\s*{",
		"import\\s+[\\w{}\\s]+from", "export\\s+", "class\\s+\\w+",

		// Estruturas comuns em várias linguagens
		"if\\s*\\(.+\\)\\s*{", "for\\s*\\(.+\\)\\s*{", "while\\s*\\(.+\\)\\s*{",
		"switch\\s*\\(.+\\)\\s*{", "\\}\\s*else\\s*\\{",

		// Sintaxe de encerramento de blocos multilinha
		"};", "});", "});",
	}

	for _, pattern := range codePatterns {
		matched, _ := regexp.MatchString(pattern, content)
		if matched {
			return true
		}
	}

	// Contar chaves de abertura e fechamento para detectar blocos de código
	openBraces := strings.Count(content, "{")
	closeBraces := strings.Count(content, "}")

	// Se há várias chaves balanceadas, provavelmente é código
	return openBraces > 1 && closeBraces > 1
}

// getCriticalInput obtém entrada do usuário para decisões críticas
func (a *AgentMode) getCriticalInput(prompt string) string {
	// LÓGICA DO LINER REMOVIDA
	cmd := exec.Command("stty", "sane")
	cmd.Stdin = os.Stdin
	_ = cmd.Run()

	fmt.Print("\n")
	fmt.Print(prompt)

	reader := bufio.NewReader(os.Stdin)
	response, _ := reader.ReadString('\n')
	return strings.TrimSpace(response)
}

// askUserIfInteractive pergunta ao usuário se um comando deve ser executado em modo interativo
func (a *AgentMode) askUserIfInteractive(cmd string, contextInfo CommandContextInfo) bool {
	// Se for claramente código ou arquivo de log, não perguntar ao usuário
	if contextInfo.SourceType == SourceTypeFile && hasCodeStructures(cmd) {
		return false
	}

	// Caso contrário, perguntar ao usuário
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
	tmpfile, err := os.CreateTemp("", "agent-edit-*.sh")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(tmpfile.Name()) }()

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

	edited, err := os.ReadFile(tmpfile.Name())
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

	a.cli.animation.ShowThinkingAnimation(a.cli.Client.GetModelName())
	aiResponse, err := a.cli.Client.SendPrompt(ctx, retryPrompt, a.cli.history, 0)
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

// requestLLMWithPreExecutionContext envia o comando sugerido e um contexto adicional do usuário
// para a LLM, pedindo que ela refine ou gere um novo comando ANTES da execução.
func (a *AgentMode) requestLLMWithPreExecutionContext(ctx context.Context, originalCommand, userContext string) ([]CommandBlock, error) {
	// Criar um novo contexto com timeout
	newCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var prompt strings.Builder
	prompt.WriteString("O comando que você sugeriu foi:\n```\n")
	prompt.WriteString(originalCommand)
	prompt.WriteString("\n```\n\n")
	prompt.WriteString("Antes de executá-lo, o usuário forneceu o seguinte contexto ou instrução adicional:\n")
	prompt.WriteString(userContext)
	prompt.WriteString("\n\nPor favor, revise o comando sugerido com base neste novo contexto. Se necessário, modifique-o ou sugira um novo conjunto de comandos. Apresente os novos comandos no formato executável apropriado.")

	// Adiciona o prompt como novo turno "user"
	a.cli.history = append(a.cli.history, models.Message{
		Role:    "user",
		Content: prompt.String(),
	})

	a.cli.animation.ShowThinkingAnimation(a.cli.Client.GetModelName())
	aiResponse, err := a.cli.Client.SendPrompt(newCtx, prompt.String(), a.cli.history, 0)
	a.cli.animation.StopThinkingAnimation()
	if err != nil {
		fmt.Println("❌ Erro ao pedir refinamento à IA:", err)
		return nil, err
	}

	// Adiciona resposta da IA ao histórico
	a.cli.history = append(a.cli.history, models.Message{
		Role:    "assistant",
		Content: aiResponse,
	})

	// Processa a resposta para extrair os novos comandos
	blocks := a.extractCommandBlocks(aiResponse)
	a.displayResponseWithoutCommands(aiResponse, blocks)
	return blocks, nil
}
