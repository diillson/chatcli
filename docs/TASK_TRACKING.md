# Sistema de Checklist Progressivo no Modo Coder

O ChatCLI agora inclui um sistema de tracking de tarefas que permite à IA criar, acompanhar e replanejar tarefas automaticamente.

## Como Funciona

1. **Criação de Plano**: A IA cria uma lista numerada de tarefas no `<reasoning>`
2. **Acompanhamento**: Conforme executa ações, as tarefas são marcadas como:
   - `[ ]` - Pendente
   - `[>]` - Em andamento
   - `[x]` - Concluída
   - `[!]` - Falhada
3. **Replanejamento**: Após 3 falhas consecutivas, o sistema solicita um novo plano

## Exemplo de Uso

```bash
chatcli /coder "crie um arquivo HTML com um formulário simples"
```

**Resposta da IA**:

```markdown
<reasoning>
1. Criar estrutura HTML básica
2. Adicionar formulário com campos de nome e email
3. Adicionar estilo CSS básico
4. Validar o arquivo criado
</reasoning>

<tool_call name="@coder" args="write --file index.html --encoding base64 --content [...]" />
```

**Progresso Renderizado**:

```
Plano de Acao:
> [x] 1. Criar estrutura HTML básica
  [>] 2. Adicionar formulário com campos de nome e email
  [ ] 3. Adicionar estilo CSS básico
  [ ] 4. Validar o arquivo criado

Progresso: 1/4 concluídas
```

## Arquitetura

### Componentes

1. **TaskTracker** (`./cli/agent/task_tracker.go`)
   - Gerencia o ciclo de vida das tarefas
   - Parseia reasoning e extrai tarefas numeradas
   - Mantém metadados de status, tentativas e erros

2. **TaskIntegration** (`./cli/agent/task_integration.go`)
   - Integra o tracking no loop principal
   - Funções helper para marcar tarefas como concluídas/falhadas

3. **AgentMode Integration** (`./cli/agent_mode.go`)
   - Parseia reasoning e atualiza plano
   - Renderiza progresso visual
   - Atualiza status após cada ação
   - Detecta e solicita replanejamento

### Fluxo de Execução

```
1. AI gera <reasoning> com lista numerada
   ↓
2. TaskTracker.ParseReasoning() extrai tarefas
   ↓
3. Renderiza plano visual
   ↓
4. AI executa <tool_call>
   ↓
5. MarkCurrentAs(completed/failed)
   ↓
6. Atualiza visualização
   ↓
7. Se >=3 falhas: Solicita replanejamento
```

## Benefícios

- ✅ **Transparência**: O usuário vê o que a IA está pensando e fazendo
- ✅ **Acompanhamento**: Progresso em tempo real
- ✅ **Resilência**: Replanejamento automático após falhas
- ⌅ **Depuração**: Fácil identificar onde falhou
- 🚁 **Melhor UX**: Feedback visual riquíssimo

## Exemplo de Replanejamento

**Plano Original**:
```
1. [ ] Criar arquivo config.yaml
2. [ ] Rodar docker compose
3. [ ] Verificar serviços
```

**Após 3 falhas**:
```
ATENÇÃO: Múltiplas falhas detectadas. Replanejamento necessário!
```

**Novo Plano**:
```
<reasoning>
1. Verificar se Docker está instalado
2. Criar arquivo config.yaml com validação
3. Rodar docker compose com logs
4. Verificar serviços individualmente
</reasoning>
```

## Customização

Os prompts do Coder foram atualizados para incluir instruções de checklist:

```
Regras OBRIGATÓRIAS:
1) Antes de agir, escreva um <reasoning> curto com uma LISTA DE TAREFAS numeradas.
   - Cada tarefa deve ser uma linha independente
   - Conforme concluir, marque com [x] no inicio
   - Se houver erro, crie uma NOVA lista replanejada
```

## Conclusão

O sistema de checklist progressivo torna o modo Coder:
- Mais transparente
- Mais confiável
- Mais resiliente
- Mais fácil de depurar

A IA agora é capaz de:
1. ★ Planejar antes de agir
2. ★ Acompanhar seu próprio progresso
3. ★ Reconhecer falhas e replanejar
4. ★ Manter uma visão clara do objetivo
