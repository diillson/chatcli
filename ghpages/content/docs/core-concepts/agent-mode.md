+++
title = "Modo Agente: Execução de Tarefas"
linkTitle = "Modo Agente (/agent)"
weight = 30
description = "Aprenda a delegar tarefas completas para o ChatCLI, que irá planejar e executar comandos para você."
icon = "smart_toy"
+++

O Modo Agente transforma o **ChatCLI** de um assistente passivo em um **executor proativo**. Em vez de apenas pedir informações, você delega uma tarefa completa, e a IA cria, apresenta e—com sua aprovação—executa um plano de ação para concluí-la.

É a funcionalidade ideal para tarefas que envolvem múltiplos passos no terminal.

---

## Como Iniciar o Modo Agente

Para ativar o Modo Agente, use o comando `/agent` ou seu atalho `/run`, seguido da sua tarefa em linguagem natural.

{{< command >}}
/agent encontre todos os arquivos de log no meu diretório home que foram modificados nas últimas 24 horas e copie-os para uma pasta chamada 'logs_recentes'.
{{< /command >}}

Após receber sua instrução, a IA analisará o pedido e responderá com um **Plano de Ação**: uma lista de comandos estruturados para sua revisão.

---

## O Ciclo do Agente: Do Plano à Execução

O Modo Agente opera em um ciclo interativo que lhe dá total controle. O diagrama abaixo ilustra o fluxo:

```mermaid
sequenceDiagram
    actor User as Usuário
    participant CLI as ChatCLI
    participant Agent as Agente
    participant LLM as IA

    User->>CLI: /agent <sua tarefa>
    CLI->>Agent: Inicia com a tarefa
    Agent->>LLM: Solicita plano de ação
    LLM-->>Agent: Retorna plano estruturado (comandos + explicações)
    Agent->>CLI: Renderiza a interface do Plano de Ação
    CLI-->>User: Exibe o plano para revisão

    loop Revisão e Execução
        User->>CLI: Escolhe uma ação (ex: '1', 'a', 'e1')
        CLI->>Agent: Executa comando(s) aprovado(s)
        Agent-->>CLI: Captura saída (stdout/stderr)
        CLI-->>User: Exibe resultado e atualiza o plano
    end
```
--------

## A Interface do Plano de Ação

Após o planejamento da IA, você verá uma tela dedicada com duas visualizações que podem ser alternadas com a tecla  p .

#### Visão Compacta (Padrão)

Ideal para uma visão geral do fluxo, mostrando o status e a primeira linha de cada comando.
```plaintext
📋 PLANO (visão compacta)
  ✅ #1: Criar o diretório de destino — mkdir -p logs_recentes
  ⏳ #2: Encontrar e copiar os arquivos — find ~ -name "*.log" -mtime -1 -exec cp {} logs_recentes/ \;
```
#### Visão Completa

Fornece um "cartão" detalhado para cada comando, incluindo a descrição gerada pela IA, tipo, análise de risco e o bloco de código completo.
```plaintext
🔷 COMANDO #2: Encontrar e copiar os arquivos
    Tipo:   shell
    Risco:  Seguro
    Status: Pendente
    Código:
      $ find ~ -name "*.log" -mtime -1 -exec cp {} logs_recentes/ \;
```
--------

## O Menu Interativo: Seu Centro de Controle

Este menu permite que você gerencie a execução do plano com precisão.
```bash
Ação  │ Descrição                                                                         
──────┼───────────────────────────────────────────────────────────────────────────────────
[N]   │ Executar Comando N: Executa um único passo do plano (ex:  1 ).                    
a     │ Executar Todos: Executa todos os comandos pendentes em sequência.                 
eN    │ Editar Comando N: Abre o comando  N  em um editor para modificação.               
tN    │ Testar (Dry-Run): Simula a execução do comando  N  sem fazer alterações.          
cN    │ Continuar de N: Envia a saída do comando  N  para a IA e pede os próximos passos.
pcN   │ Contexto Pré-Execução: Adiciona informações para a IA refinar o comando  N .      
acN   │ Contexto Pós-Execução: Envia a saída do comando  N  com um novo contexto.         
vN    │ Ver Saída: Abre a saída completa do comando  N  em um pager ( less ).             
wN    │ Salvar Saída: Salva a saída do comando  N  em um arquivo temporário.              
p     │ Alternar Plano: Muda a visualização entre  COMPACTO  e  COMPLETO .                
r     │ Redesenhar a Tela: Limpa a tela.                                                  
q     │ Sair (Quit): Encerra o Modo Agente e retorna ao chat normal.
```
{{< notice "info" >}}
**Dica de Fluxo de Trabalho:** Use  tN  (testar) para verificar o que um comando complexo fará. Se o resultado for o esperado, execute-o com  N . Se algo der errado, use  cN  (continuar) para pedir à IA que corrija o plano com base no erro.
{{< /notice >}}

--------

## Segurança em Primeiro Lugar

O Modo Agente foi construído com a segurança como prioridade.

{{< notice "warning" >}}
**Validação Automática:** Comandos considerados perigosos ( rm -rf ,  sudo ,  mkfs ,  dd ) são bloqueados por padrão. O ChatCLI exigirá uma confirmação explícita e detalhada antes de permitir sua execução.
{{< /notice >}}

Você sempre tem a palavra final. Nenhum comando é executado sem sua aprovação.

--------

## Próximos Passos

Agora que você domina a execução de tarefas, que tal aprender a salvar e reutilizar seu trabalho em diferentes projetos?

➡️ Próximo: [Gerenciamento de Sessões](/chatcli/docs/features/session-management/)