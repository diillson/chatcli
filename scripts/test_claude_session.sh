#!/bin/bash

# Script para testar a API de sessão do Claude

# Verifica se a chave API está configurada
if [ -z "$CLAUDEAI_API_KEY" ]; then
    echo "Erro: CLAUDEAI_API_KEY não está definida"
    exit 1
fi

# Definir variáveis de ambiente
export CLAUDEAI_USE_SESSION=true
export LOG_LEVEL=debug
export LLM_PROVIDER=CLAUDEAI

echo "Iniciando ChatCLI com API de Sessão do Claude..."
echo "Use as seguintes mensagens para testar:"
echo "1. Olá, vamos explorar um tema complexo: a teoria da relatividade."
echo "2. Explique o conceito de dilatação do tempo."
echo "3. Como isso se relaciona com o que discutimos no início?"
echo "4. Defina uma variável x = 5."
echo "5. Qual é o valor de x + 10?"
echo ""
echo "Estas mensagens testam se o contexto está sendo mantido corretamente pelo servidor."
echo ""

# Iniciar a aplicação
./chatcli