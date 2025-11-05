+++
title = "Modo Agente: Execução de Tarefas"
linkTitle = "Modo Agente"
weight = 30
description = "Aprenda a delegar tarefas completas para o ChatCLI, que irá planejar e executar comandos para você."
+++

O Modo Agente eleva o **ChatCLI** de uma simples ferramenta de chat para um verdadeiro assistente autônomo. Em vez de apenas pedir informações, você pode delegar uma tarefa completa, e a IA irá criar, apresentar e, com sua aprovação, executar um plano de ação para concluí-la.

---

## Como Iniciar o Modo Agente

Para ativar o Modo Agente, use os comandos `/agent` ou seu atalho `/run`, seguido da tarefa que você deseja realizar.

**Sintaxe:**
```bash
/agent <sua tarefa em linguagem natural>
# Ou
/run <sua tarefa em linguagem natural>
```
### Exemplo Prático:

/agent encontre todos os arquivos de log no meu diretório home que foram modificados nas últimas 24 horas e copie-os para uma pasta chamada 'logs_recentes'.

###### Após receber sua instrução, a IA analisará o pedido e responderá com um Plano de Ação, que é uma lista de comandos estruturados para você revisar.

--------

## O Ciclo do Agente

O Modo Agente opera em um ciclo interativo que lhe dá total controle:

1. Planejamento: A IA cria um plano de execução detalhado.
2. Revisão: O ChatCLI exibe o plano para você em uma interface interativa.
3. Ação: Você decide o que fazer: executar um comando, todos os comandos, editar, simular ou pedir uma continuação.
4. Execução e Observação: O ChatCLI executa os comandos aprovados e captura a saída.
5. Reiteração: Com base no resultado, você pode continuar o processo, pedir uma correção ou finalizar a tarefa.

--------

## A Interface do Plano de Ação

Após o planejamento, você verá uma tela dedicada com duas visualizações principais que podem ser alternadas com a tecla  p .

#### Visão Compacta (Padrão)

Mostra uma lista resumida de cada passo, ideal para ter uma visão geral do fluxo.

📋 PLANO (visão compacta)
  - ✅ #1: Criar o diretório de destino — mkdir -p logs_recentes
  - ⏳ #2: Encontrar e copiar os arquivos — find ~ -name "*.log" -mtime -1 -exec cp ...

#### Visão Completa

Fornece um cartão detalhado para cada comando, incluindo descrição, tipo de linguagem, risco de segurança e o bloco de código completo.

- 🔷 COMANDO #1: Criar o diretório de destino
    Tipo: shell
    Risco: Seguro
    Status: OK
    Código:
      $ mkdir -p logs_recentes

--------

## O Menu Interativo de Ações

Este é o seu centro de controle no Modo Agente. Após cada plano ou execução, você pode escolher uma das seguintes ações:
```bash
Comando │ Ação                  │ Descrição                                                                        
────────┼───────────────────────┼──────────────────────────────────────────────────────────────────────────────────
[N]     │ Executar Comando N    │ Executa um único comando do plano (ex:  1  para executar o primeiro).            
a       │ Executar Todos (All)  │ Executa todos os comandos pendentes na sequência, parando se ocorrer um erro.    
eN      │ Editar Comando N      │ Abre o comando N em um editor para que você possa modificá-lo antes de executar.
tN      │ Testar (Dry-Run)      │ Simula a execução do comando N sem realmente fazer alterações no sistema.        
cN      │ Continuar de N        │ Usa a saída do comando N para pedir à IA os próximos passos ou uma correção.     
pcN     │ Contexto Pré-Execução │ Adiciona mais informações para a IA refinar o comando N antes de executá-lo.     
acN     │ Contexto Pós-Execução │ Envia a saída do comando N junto com um novo contexto para análise da IA.        
vN      │ Ver Saída de N        │ Abre a saída completa e não truncada do comando N em um pager ( less ).          
wN      │ Salvar Saída de N     │ Salva a saída completa do comando N em um arquivo de log temporário.             
p       │ Alternar Plano        │ Muda a visualização do plano entre  COMPACTO  e  COMPLETO .                      
r       │ Redesenhar a Tela     │ Limpa e redesenha a tela, útil se a saída de um comando poluir a visualização.   
q       │ Sair (Quit)           │ Encerra o Modo Agente e retorna ao chat interativo normal.
```
--------

## Segurança em Primeiro Lugar

Para sua segurança, o ChatCLI possui um validador integrado:

- Comandos Perigosos: Comandos como  rm -rf ,  sudo  e  mkfs  são automaticamente bloqueados. O agente pedirá uma confirmação explícita e detalhada antes de prosseguir.
- Controle Total: Nenhum comando é executado sem sua aprovação explícita. Você sempre tem a palavra final.

--------

## Próximos Passos

Agora que você domina a execução de tarefas, que tal aprender a salvar e reutilizar seu trabalho?

➡️ Próximo: [Gerenciamento de Sessões](/docs/features/session-management/)


---