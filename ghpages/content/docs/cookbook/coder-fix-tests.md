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
<tool_call name="@coder" args="{&quot;cmd&quot;:&quot;test&quot;,&quot;args&quot;:{&quot;dir&quot;:&quot;.&quot;}}"/>
```

## 3. O ChatCLI Executa

O resultado do comando é devolvido para a IA.

## 4. A IA Continua (Leitura)

```xml
<reasoning>
Ler o arquivo com erro.
</reasoning>
<tool_call name="@coder" args="{&quot;cmd&quot;:&quot;read&quot;,&quot;args&quot;:{&quot;file&quot;:&quot;main.go&quot;}}"/>
```

## 5. Aplicando a Correção (Patch)

```xml
<reasoning>
Aplicar patch para corrigir lógica.
</reasoning>
<tool_call name="@coder" args="{&quot;cmd&quot;:&quot;patch&quot;,&quot;args&quot;:{&quot;file&quot;:&quot;main.go&quot;,&quot;encoding&quot;:&quot;base64&quot;,&quot;search&quot;:&quot;base64_old&quot;,&quot;replace&quot;:&quot;base64_new&quot;}}"/>
```

> Nota: Todo esse fluxo acontece autonomamente dentro do modo `/coder`.
