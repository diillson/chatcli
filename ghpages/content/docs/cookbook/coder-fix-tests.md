---
title: "Corrigir testes com /coder"
description: "Receita prática: veja como a IA usa o plugin @coder para corrigir testes autonomamente."
weight: 50
---

Nesta receita, você verá como a IA chama ferramentas de engenharia (`@tool_call`) para corrigir testes.

## 1. O Usuário Define o Objetivo

{{< command >}}
/coder corrija os testes falhando
{{< /command >}}

## 2. A IA Investiga (Loop ReAct)

A IA analisa o pedido e chama o plugin:

```xml
<reasoning>
Rodar go test para ver erros.
</reasoning>
<tool_call name="@coder" args="{\"cmd\":\"test\",\"args\":{\"dir\":\".\"}}"/>
```

## 3. O ChatCLI Executa

O resultado do comando é devolvido para a IA.

## 4. A IA Continua (Leitura)

```xml
<reasoning>
Ler o arquivo com erro.
</reasoning>
<tool_call name="@coder" args="{\"cmd\":\"read\",\"args\":{\"file\":\"main.go\"}}"/>
```

## 5. Aplicando a Correção (Patch)

```xml
<reasoning>
Aplicar patch para corrigir lógica.
</reasoning>
<tool_call name="@coder" args="{\"cmd\":\"patch\",\"args\":{\"file\":\"main.go\",\"encoding\":\"base64\",\"search\":\"base64_old\",\"replace\":\"base64_new\"}}"/>
```

> Nota: Todo esse fluxo acontece autonomamente dentro do modo `/coder`.
