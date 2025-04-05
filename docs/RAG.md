# Guia do Sistema RAG (Retrieval Augmented Generation)
    
## O que é RAG?
    
RAG (Retrieval Augmented Generation) é uma técnica que melhora as respostas de modelos de linguagem ao incorporar conhecimento externo relevante. No ChatCLI, o RAG permite analisar projetos de código grandes recuperando apenas as partes relevantes para cada consulta específica.
    
## Como Funciona
    
1. **Indexação**: O sistema divide o código em pequenos pedaços (chunks), gera representações vetoriais (embeddings) e os armazena
2. **Recuperação**: Quando você faz uma pergunta, o sistema encontra os chunks mais relevantes com base na similaridade semântica
3. **Geração**: Os chunks recuperados são enviados junto com sua pergunta para o LLM, que gera uma resposta informada pelo código relevante
    
## Comandos Principais
    
### Indexando um Projeto
    
```bash
   /rag index ~/caminho/do/projeto
```

Este comando:

- Escaneia recursivamente o diretório especificado
- Processa arquivos compatíveis (código, configuração, documentação)
- Divide o conteúdo em chunks gerenciáveis
- Gera embeddings para cada chunk
- Armazena tudo no banco de dados vetorial em memória

Dica: A indexação é a etapa mais demorada, especialmente para projetos grandes. Aguarde a conclusão completa antes de fazer consultas.

### Consultando o Código Indexado

Há duas maneiras de consultar o código indexado:

#### 1. Consulta Direta

```bash
/rag query Como funciona o sistema de autenticação neste projeto?
```

Este método:

- Recupera os chunks mais relevantes para sua consulta
- Formata-os em um contexto estruturado
- Envia o contexto junto com sua pergunta para o LLM
- Exibe a resposta completa formatada

#### 2. Consulta Inline

```bash
Explique detalhadamente como @inrag o sistema de caching funciona
```

Este método:

- Recupera os chunks relevantes com base no texto após  @inrag
- Adiciona esses chunks como contexto adicional à sua pergunta principal
- Permite perguntas mais complexas que incorporam conhecimento do código
- É ótimo para conversas mais naturais que se referem ao código

### Limpando o Índice

```bash
/rag clear
```

Este comando:

- Remove completamente todos os documentos e embeddings do banco de dados em memória
- Libera memória
- É útil antes de indexar um novo projeto ou após terminar sua análise

### Comando Unificado para Projetos

```bash
/projeto ~/caminho/do/projeto Explique a arquitetura do sistema
```

Este comando combina indexação e consulta em uma única operação:

- Indexa o projeto especificado (se ainda não estiver indexado)
- Executa a consulta imediatamente após a indexação
- É conveniente para análises pontuais de projetos

## Otimizações e Limitações

### Otimizações

- Processamento em Chunks: Divide arquivos grandes em pedaços gerenciáveis
- Seleção Inteligente: Analisa e seleciona apenas as partes mais relevantes do código
- Embeddings Eficientes: Utiliza técnicas de compressão para reduzir o uso de memória

### Limitações

- Armazenamento em Memória: O índice é mantido apenas em memória e não persiste entre sessões
- Limites de Tamanho: Projetos extremamente grandes podem precisar ser divididos
- Dependência do Provedor: A qualidade dos embeddings pode variar dependendo do provedor LLM

## Detalhes Técnicos

O sistema RAG do ChatCLI é composto por:

1. Chunker: Divide texto em segmentos semânticos significativos
2. Embeddings Generator: Cria representações vetoriais do texto
3. Vector Database: Armazena e realiza buscas por similaridade
4. Retriever: Recupera o conteúdo mais relevante para cada consulta
5. RAG Manager: Coordena todos os componentes

## Comparação de Métodos de Consulta
```bash
Método          │ Comando                         │ Melhor Para                         │ Limitações                                    
─────────────────┼─────────────────────────────────┼─────────────────────────────────────┼───────────────────────────────────────────────
Consulta Direta │  /rag query <pergunta>          │ Perguntas focadas sobre o código    │ Menos flexível na formulação do prompt        
Consulta Inline │  Pergunta @inrag contexto       │ Integração em conversas mais amplas │ Pode ser menos preciso em consultas complexas
Comando Projeto │  /projeto <caminho> <pergunta>  │ Análises pontuais sem pré-indexação │ Mais lento para múltiplas consultas
```

## Exemplos de Uso Avançado

### Análise Arquitetural
```bash
/rag query Explique a arquitetura deste sistema, identificando os principais padrões de design
```

### Integração com Contexto Específico
```bash
Como poderia melhorar @inrag sistema de tratamento de erros considerando as práticas modernas de Go?
```

### Revisão Focada em Segurança
```bash
/projeto ~/aplicacao Identifique possíveis vulnerabilidades de segurança no código
```

## Suporte a Provedores LLM

O sistema RAG funciona com os seguintes provedores:

• OpenAI: Utiliza a API de embeddings nativa da OpenAI para indexação
• Claude AI: Utiliza uma implementação otimizada para funcionar com a API Claude
• StackSpot: Compatível, mas com funcionalidades limitadas

## Resolução de Problemas

P: Obtenho "Nenhum conteúdo relevante encontrado" em todas as consultas
R: Verifique se o projeto foi indexado corretamente com  /rag index  e se a consulta está relacionada ao código.

P: A indexação está muito lenta
R: Projetos grandes podem levar tempo para serem processados. Considere indexar apenas subdiretórios específicos.

P: Recebo erro de limite de tokens
R: O sistema tenta lidar com isso automaticamente, mas para projetos muito grandes, use diretórios mais específicos.

P: Quero indexar um novo projeto
R: Execute  /rag clear  para limpar o índice atual antes de indexar um novo projeto.


## Explicação Detalhada do Comando `/projeto` um dos que você mais irá utilizar.
    
O comando `/projeto` é um utilitário poderoso que combina a indexação e consulta em uma única operação, sendo ideal para análises pontuais ou exploratórias de uma base de código.
    
# Comando `/projeto`
    
## Visão Geral
    
O comando `/projeto` é uma funcionalidade "tudo-em-um" que permite analisar rapidamente um projeto de código sem passar por etapas separadas de indexação e consulta.
    
```bash
/projeto <caminho_do_projeto> <sua_pergunta_ou_tarefa>
```

## Como Funciona

Quando você executa o comando  /projeto , o ChatCLI:

1. Inicializa o Sistema RAG: Configura o mecanismo RAG com o provedor LLM atual
2. Indexa o Projeto: Analisa e indexa automaticamente o diretório especificado
3. Consulta o Índice: Busca chunks de código relevantes com base em sua pergunta
4. Gera Resposta: Envia o contexto recuperado junto com sua pergunta para o LLM
5. Exibe Resultados: Apresenta a resposta formatada do LLM

## Casos de Uso Ideais

O comando  /projeto  é especialmente útil para:

• Análise Única: Quando você precisa analisar um projeto rapidamente sem manter o índice
• Exploração Inicial: Para entender a estrutura e funcionamento básico de uma base de código
• Diagnóstico Rápido: Para identificar problemas ou padrões em um projeto
• Demonstrações: Ideal para mostrar a capacidade do RAG sem múltiplos comandos

## Exemplos Práticos

### Entendendo a Arquitetura

    /projeto ~/meuprojeto Explique a arquitetura geral deste sistema e como os componentes se relacionam

### Identificando Padrões de Design

    /projeto ~/aplicacao-web Quais padrões de design são utilizados neste código? Dê exemplos específicos

### Análise de Qualidade de Código

    /projeto ~/legacy-code Identifique problemas e dívidas técnicas neste código, sugerindo melhorias

### Documentação Automática

    /projeto ~/biblioteca Gere uma documentação README.md clara para esta biblioteca

## Considerações Importantes

1. Desempenho: Para projetos grandes, o comando pode levar mais tempo para completar todas as etapas
2. Uso Repetido: Se você planeja fazer múltiplas consultas sobre o mesmo projeto, é mais eficiente usar  /rag index  uma vez e depois múltiplos  /rag query
3. Memória: O índice gerado permanece na memória até que você use  /rag clear  ou encerre o ChatCLI
4. Provedores: Funciona melhor com OpenAI e Claude, pois esses provedores têm melhor suporte à análise de código

## Limitações

- Projetos muito grandes podem atingir limites de contexto ou token
- A análise completa depende da qualidade e relevância do código recuperado
- Para análises mais complexas ou específicas, o fluxo de indexação e consulta separados pode oferecer mais controle