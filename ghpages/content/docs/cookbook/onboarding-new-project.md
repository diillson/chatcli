+++
title = "Receita: Entendendo um Projeto de Código"
linkTitle = "Entender Novo Projeto"
weight = 30
description = "Use uma combinação de modos do @file para rapidamente entender a arquitetura e o fluxo de uma base de código desconhecida."
+++

Entrar em um novo projeto pode ser desafiador. Onde estão os arquivos principais? Qual é a arquitetura? Como funciona o fluxo de autenticação? Esta receita mostra como usar o **ChatCLI** para acelerar drasticamente seu processo de onboarding.

### O Problema

Você clonou um repositório com centenas de arquivos e precisa se tornar produtivo o mais rápido possível. Ler tudo é inviável, e a documentação pode estar desatualizada.

### Ingredientes

-   Um repositório de código clonado em sua máquina local.

---

### Passo a Passo

Este fluxo de trabalho usa uma abordagem "de cima para baixo": primeiro obtemos uma visão geral da arquitetura e depois mergulhamos em detalhes específicos.

#### Passo 1: A Visão Panorâmica com `@file --mode=summary`

O primeiro passo é obter um mapa do território. O modo `summary` é perfeito para isso, pois ele lista a estrutura de arquivos e diretórios sem enviar o conteúdo do código.

{{< command >}}
@file --mode=summary . > Descreva a arquitetura geral deste projeto com base na estrutura de arquivos. Quais parecém ser os componentes mais importantes?
{{< /command >}}

**O que esperar da IA:**
A IA retornará uma análise de alto nível, como:
```markdown
  Este parece ser um projeto de backend em Go. 
  O diretório `/cmd` sugere que há múltiplos binários. 

  A pasta `/internal/api` provavelmente contém os handlers das rotas HTTP, 
   enquanto `/internal/database` gerencia a lógica de acesso ao banco de dados. 
  
  O arquivo `docker-compose.yml` indica que o projeto usa Docker para o ambiente de desenvolvimento.
```

Com isso, você já tem um mapa mental de onde procurar as coisas.

---

#### Passo 2: Investigação Específica com `@file --mode=smart`

Agora que você tem uma visão geral, pode fazer perguntas específicas. O modo `smart` é ideal aqui, pois você não precisa saber quais arquivos contêm a resposta. A IA irá encontrá-los para você.

Vamos investigar o fluxo de autenticação.

{{< command >}}
@file --mode=smart ./internal/ > Explique o fluxo de autenticação de ponta a ponta, desde a rota da API até a validação do token.
{{< /command >}}

**Como funciona:**
1.  O ChatCLI envia a lista de todos os arquivos dentro de `./internal/` para a IA.
2.  A IA, com base na pergunta "fluxo de autenticação", identifica que arquivos como `auth_handler.go`, `jwt_service.go` e `user_repository.go` são provavelmente os mais relevantes.
3.  O ChatCLI então envia o conteúdo **apenas desses arquivos selecionados**.
4.  A IA responde à sua pergunta com base no código real dos arquivos mais importantes.

---

#### Passo 3: Mergulho Profundo em um Arquivo com `@file --mode=full`

Durante sua investigação, você pode querer entender um arquivo específico em detalhes. Agora, use o modo `full` (ou simplesmente omita o modo, pois é o padrão).

{{< command >}}
@file ./internal/api/middleware/auth.go > Refatore este middleware para seguir as melhores práticas de Go, adicionando tratamento de erros mais robusto e logging.
{{< /command >}}

A IA agora tem o conteúdo completo do arquivo e pode realizar tarefas complexas como refatoração, documentação ou geração de testes.

{{< notice "success" >}}
**Resultado:** Em menos de 15 minutos, você passou de uma visão zero do projeto para:
1. Entender a arquitetura de alto nível.
2. Mapear um fluxo de negócio crítico (autenticação).
3. Analisar e até mesmo refatorar um componente de código específico.
{{< /notice >}}

Este fluxo de trabalho transforma horas ou dias de exploração manual em uma sessão de perguntas e respostas focada e eficiente.

--------