# ChatCLI - Guia de APIs de Sessão

## Visão Geral
    
O ChatCLI agora suporta o uso de APIs de sessão da OpenAI e Claude, que mantêm o contexto da conversa no lado do servidor. Isso traz várias vantagens:
    
1. **Economia de Tokens**: Não é necessário enviar todo o histórico de conversa a cada requisição
2. **Conversas Mais Longas**: O limite de contexto não é mais uma restrição local
3. **Melhor Desempenho**: Respostas mais rápidas por não processar contextos extensos
    
## Como Usar as APIs de Sessão
    
### 1. Configuração via Variáveis de Ambiente
    
Você pode habilitar as APIs de sessão definindo as seguintes variáveis de ambiente:
    
```bash
# Para OpenAI
export OPENAI_USE_SESSION=true
    
# Para Claude
export CLAUDEAI_USE_SESSION=true
```
Estas configurações podem ser incluídas no seu arquivo  .env  ou definidas diretamente no terminal.

### 2. Seleção Interativa

Ao trocar de provedor usando o comando  /switch , o ChatCLI perguntará se você deseja usar a API de sessão:

Selecione o provedor pelo número: 1
Deseja usar a API de sessões para manter o contexto no provedor de LLM? (s/n): s
Usando API de sessões OpenAI.

### 3. Confirmação Visual

Quando as APIs de sessão estão em uso, você verá uma mensagem clara no terminal:

🔄 Usando API de sessões OpenAI para manter o contexto no provedor de LLM.

## Como Funcionam as APIs de Sessão

### OpenAI Assistants API

A API Assistants da OpenAI funciona através de:

1. Assistants: Representam a IA que responderá às suas perguntas
2. Threads: Representam uma conversa contínua
3. Messages: Mensagens dentro de um thread
4. Runs: Execuções do assistente para processar o thread e gerar respostas

O ChatCLI gerencia todo esse fluxo automaticamente:

• Cria um assistente e thread ao iniciar a sessão
• Adiciona suas mensagens ao thread
• Executa o assistente para gerar respostas
• Mantém o thread durante toda a conversa

### Claude Messages API

A API Messages da Claude é mais simples e usa:

1. Conversation ID: Um identificador de conversa que mantém o contexto
2. Messages: Mensagens individuais enviadas na conversa

Ao fornecer o mesmo  conversation_id  em requisições subsequentes, a Claude mantém automaticamente o histórico da conversa e o contexto.

## Limitações

• Persistência entre Sessões: As sessões não são automaticamente retomadas ao reiniciar o ChatCLI
• Custos: As APIs de sessão podem ter custos diferentes das APIs tradicionais (verifique os preços atuais)
• Controle de Contexto: Você tem menos controle sobre quais partes do histórico são mantidas ou descartadas

## Solução de Problemas

### Problemas Comuns

1. Perda de Contexto: Se a sessão expirar ou for interrompida, o ChatCLI tentará criar uma nova automaticamente
2. Erros de API: Em caso de falhas, o ChatCLI tenta:
   • Reinicializar a sessão
   • Tentar novamente a requisição
   • Reverter para o modo tradicional se necessário
3. Transferência entre Provedores: Ao trocar de provedor, o contexto anterior é perdido (limitação da API)

### Logs para Depuração

Para obter logs detalhados sobre as sessões, defina:

    export LOG_LEVEL=debug

Isso mostrará informações detalhadas sobre inicialização de sessão, envio de mensagens e respostas.

## Exemplos de Uso

### Conversa sobre um Tema Complexo

As APIs de sessão são particularmente úteis para conversas longas sobre temas complexos, onde o contexto anterior é importante:

Você: Vamos discutir estruturas de dados avançadas
ChatCLI: Claro! Quais estruturas de dados você gostaria de explorar primeiro?
    
Você: Árvores B+
ChatCLI: [Explicação detalhada sobre Árvores B+]
    
Você: Como elas se comparam com Árvores Vermelho-Preto?
ChatCLI: [Comparação detalhada mantendo o contexto anterior]
    
... [muitas mensagens depois] ...
    
Você: Voltando àquela primeira estrutura que discutimos...
ChatCLI: [Resposta referenciando corretamente Árvores B+]

### Análise de Código em Várias Partes

Você: @file ~/projeto/src/core.js
ChatCLI: [Análise do arquivo]
    
Você: Agora me mostre como implementar o módulo de autenticação
ChatCLI: [Resposta que se baseia no conhecimento do arquivo core.js]
    
... [muitas interações depois] ...
    
Você: Como isto afetaria a função principal que vimos primeiro?
ChatCLI: [Resposta relacionando com o conteúdo original em core.js]
  
    
## Conclusão
    
Esta documentação fornece uma visão geral das novas APIs de sessão implementadas no ChatCLI, como usá-las e quais benefícios elas trazem. É uma adição valiosa ao projeto para que os usuários possam aproveitar ao máximo as novas funcionalidades.