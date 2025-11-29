# 📘 README - Plugin ChatCLI EKS

> Plugin de Platform Engineering para criar clusters EKS production-ready com VPC, Spot Instances, ArgoCD, Istio, Nginx, Cert-Manager e External DNS

---

## 🎯 O QUE É ESTE PLUGIN?

Um plugin completo para o ChatCLI System Plugins + AgenticAI que provisiona infraestrutura Kubernetes na AWS usando Pulumi como engine de IaC (Infrastructure as Code). Ele abstrai toda a complexidade de configurar:

* ✅ Cluster EKS com alta disponibilidade (multi-AZ)
* ✅ VPC customizada ou uso de VPC existente
* ✅ Node Groups com instâncias Spot (economia de ~70%)
* ✅ AWS Load Balancer Controller
* ✅ Nginx Ingress Controller com TLS automático
* ✅ Cert-Manager com Let's Encrypt OU Google Trust Services
* ✅ ArgoCD para GitOps
* ✅ Istio Service Mesh
* ✅ External DNS (automação Route53)
* ✅ Criptografia de secrets via AWS KMS

---

## 🚀 CASOS DE USO

### 1️⃣ DEV/QA - Cluster Minimalista (Custo ~$50/mês)

```bash
@eks create \
  --name=dev-cluster \
  --state-bucket-name=meu-projeto-dev-state \
  --node-type=t3.small \
  --min-nodes=1 \
  --max-nodes=3 \
  --use-spot
````

### 2️⃣ PRODUÇÃO - Cluster Completo com TLS (Custo \~$300/mês)

```bash
@eks create \
  --name=prod-cluster \
  --state-bucket-name=meu-projeto-prod-state \
  --node-type=t3.medium \
  --min-nodes=3 \
  --max-nodes=10 \
  --with-lb-controller \
  --with-nginx \
  --with-cert-manager \
  --base-domain=meusite.com \
  --cert-manager-email=admin@meusite.com \
  --with-external-dns \
  --with-argocd \
  --argocd-domain=argocd.meusite.com \
  --secrets-provider=awskms
```

### 3️⃣ SERVICE MESH - Observabilidade Avançada

```bash
@eks create \
  --name=mesh-cluster \
  --state-bucket-name=meu-projeto-mesh-state \
  --with-istio \
  --with-argocd \
  --secrets-provider=awskms
```

-----

## 📋 PRÉ-REQUISITOS

### 1\. Credenciais AWS Configuradas

```bash
# Opção 1: AWS CLI configurado
aws configure

# Opção 2: Variáveis de ambiente
export AWS_ACCESS_KEY_ID="sua-key"
export AWS_SECRET_ACCESS_KEY="seu-secret"
export AWS_REGION="us-east-1"
```

### 2\. Permissões IAM Necessárias

Sua conta AWS precisa de permissões para:

* ✅ **EKS** (Criar clusters, node groups)
* ✅ **EC2** (VPC, Subnets, Security Groups, NAT Gateways)
* ✅ **IAM** (Criar roles e policies)
* ✅ **S3** (Criar/deletar buckets)
* ✅ **KMS** (Criar/gerenciar chaves)
* ✅ **Route53** (Se usar External DNS)

> **Política IAM Recomendada:** `AdministratorAccess` (ou criar policy customizada baseada no menor privilégio).

### 3\. Domínio Configurado no Route53 (Opcional)

Se for usar `--with-cert-manager` ou `--with-external-dns`:

```bash
# Verificar hosted zones existentes
aws route53 list-hosted-zones

# Criar hosted zone (se necessário)
aws route53 create-hosted-zone \
  --name meusite.com \
  --caller-reference $(date +%s)
```

### 4. Pulumi CLI Instalado

Instale o Pulumi CLI: https://www.pulumi.com/docs/get-started/install/

Opções de uso, gerenciamento de estado:
- S3 ou backend local: não requer `pulumi login`.
- Pulumi Cloud (desenvolvimento ou produção): faça login uma vez (token salvo no arquivo):
  pulumi login
- Pulumi Cloud (CI/CD): defina `PULUMI_ACCESS_TOKEN` no ambiente, sem login interativo.
- Desenvolvimento Rápido e Local: state salvo local em `~/.chatcli/pulumi/<stack-name>`.

-----

## 🗄️ Backends de estado (como o plugin decide)

Ordem de decisão do backend de estado:

1. S3 (explícito)
   - Se você passar --state-bucket-name, o plugin usa:
     s3://<bucket>?region=<região>
   - O bucket e a tabela são criados/garantidos automaticamente.
   - Não exige PULUMI_ACCESS_TOKEN nem pulumi login, pois não usa a cloud para salvar o estado.

2. Backend atual do Pulumi CLI (pulumi login)
   - Se você NÃO passar --state-bucket-name, o plugin tenta reutilizar o backend já configurado via `pulumi login`:
     - Pulumi Cloud (https://api.pulumi.com): funciona se houver token salvo no ~/.pulumi/credentials.json (login prévio) ou no ambiente.
     - S3/file/azblob/gs: funciona direto (não precisa token).
   - Não exige variável extra; usa o que já está logado no CLI.

3. Fallback automático para backend local (file://)
   - Se não houver backend válido no CLI, o plugin usa:
     file://~/.chatcli/pulumi/<stack-name>
   - Evita o erro “PULUMI_ACCESS_TOKEN must be set …” em ambientes não interativos.

>Nota: quando o backend é S3, o plugin configura a URL com `region` automaticamente, garantindo lock distribuído.

### 🔀 Exemplos rápidos por backend

1) S3 (recomendado para times/CI)
```bash
# Create
@eks create \
  --name prod-eks \
  --region us-east-1 \
  --state-bucket-name meu-state-prod \
  --secrets-provider=awskms

# Delete
@eks delete \
  --name prod-eks \
  --region us-east-1 \
  --state-bucket-name meu-state-prod \
  --secrets-provider=awskms \
  --kms-key-id alias/pulumi-secrets-prod-eks
```
2. Pulumi Cloud reutilizando “pulumi login” (sem token em env)
```bash
pulumi login
pulumi whoami

@eks create \
  --name cloud-eks \
  --region us-east-1 \
  --secrets-provider=awskms

@eks delete \
  --name cloud-eks \
  --region us-east-1 \
  --secrets-provider=awskms \
  --kms-key-id alias/pulumi-secrets-cloud-eks
```
3. Pulumi Cloud em CI (sem login interativo, com token)
```bash
export PULUMI_ACCESS_TOKEN="pul-xxxxxxxx"

@eks create \
  --name ci-eks \
  --region us-east-1 \
  --secrets-provider=awskms
```
4. Fallback local (file://) para desenvolvimento rápido
```bash
# Create
@eks create \
  --name dev-eks \
  --region us-east-1 \
  --secrets-provider=passphrase \
  --config-passphrase 'minha-senha-segura'

# Delete
export PULUMI_CONFIG_PASSPHRASE='minha-senha-segura'
@eks delete \
  --name dev-eks \
  --region us-east-1 \
  --secrets-provider=passphrase
```
-----

## 🛠️ INSTALAÇÃO

### 1\. Compilar o Plugin

```bash
# Clone o repositório
git clone [https://github.com/diillson/chatcli.git](https://github.com/diillson/chatcli.git)
cd chatcli/plugins-examples/chatcli-eks

# Compilar
go build -o chatcli-eks main.go

# Tornar executável
chmod +x chatcli-eks

# Mover para diretório de plugins do ChatCLI
mv chatcli-eks ~/.chatcli/plugins/
```

### 2\. Verificar Instalação

```bash
# Ver metadados do plugin
@eks --metadata

# Ver documentação completa
@eks --schema
```

-----

## 📖 GUIA DE USO COMPLETO

### COMANDO `create` - Criar/Atualizar Cluster

#### Flags Essenciais

| Flag | Tipo | Padrão | Descrição |
| :--- | :--- | :--- | :--- |
| `--name` | string | `prod-eks` | Nome único do cluster (usado como Stack ID) |
| `--region` | string | `us-east-1` | Região AWS |
| `--state-bucket-name` | string | - | Bucket S3 para estado Pulumi (criado automaticamente) |
| `--secrets-provider` | string | `awskms` | Provider de criptografia: `awskms` ou `passphrase` |
| `--kms-key-id` | string | - | ID da chave KMS (criada automaticamente se omitido) |

#### Exemplos Práticos

##### 🔹 Exemplo 1: Cluster Básico com KMS Automático

```bash
@eks create \
  --name=meu-cluster \
  --state-bucket-name=meu-bucket-state \
  --secrets-provider=awskms
```

**O que acontece:**

* ✅ Cria bucket S3 `meu-bucket-state` (se não existir)
* ✅ Cria chave KMS `alias/pulumi-secrets-meu-cluster`
* ✅ Provisiona cluster EKS com 2 nós `t3.medium`

##### 🔹 Exemplo 2: Cluster com TLS Automático (Let's Encrypt)

```bash
@eks create \
  --name=prod-cluster \
  --state-bucket-name=prod-state \
  --with-nginx \
  --with-cert-manager \
  --base-domain=meusite.com \
  --cert-manager-email=admin@meusite.com \
  --with-external-dns \
  --secrets-provider=awskms
```

**O que você ganha:**

* 🔐 Certificados TLS automáticos para `*.meusite.com`
* 🌐 DNS automático via External DNS (cria registros no Route53)
* 🚀 Nginx como Ingress Controller
* 🔑 Secrets criptografados com AWS KMS

##### 🔹 Exemplo 3: ArgoCD Exposto com TLS

```bash
@eks create \
  --name=gitops-cluster \
  --state-bucket-name=gitops-state \
  --with-nginx \
  --with-cert-manager \
  --base-domain=meusite.com \
  --cert-manager-email=admin@meusite.com \
  --with-argocd \
  --argocd-domain=argocd.meusite.com \
  --with-external-dns \
  --secrets-provider=awskms
```

**Acesso ao ArgoCD:**

```bash
# URL de acesso
[https://argocd.meusite.com](https://argocd.meusite.com)

# Usuário padrão
admin

# Pegar senha
kubectl -n argocd get secret argocd-initial-admin-secret \
  -o jsonpath="{.data.password}" | base64 -d
```

##### 🔹 Exemplo 4: Usar Chave KMS Existente

```bash
# Listar chaves existentes
aws kms list-aliases

@eks create \
  --name=secure-cluster \
  --state-bucket-name=secure-state \
  --secrets-provider=awskms \
  --kms-key-id=alias/minha-chave-existente
```

##### 🔹 Exemplo 5: Google Trust Services (Rate Limits Maiores)

```bash
@eks create \
  --name=high-traffic-cluster \
  --state-bucket-name=traffic-state \
  --with-cert-manager \
  --base-domain=meusite.com \
  --cert-manager-email=admin@meusite.com \
  --acme-provider=google \
  --with-nginx \
  --secrets-provider=awskms
```

##### 🔹 Exemplo 6: Spot Instances (Economia de 70%)

```bash
@eks create \
  --name=cost-optimized-cluster \
  --state-bucket-name=cost-state \
  --node-type=t3.large \
  --min-nodes=3 \
  --max-nodes=10 \
  --use-spot \
  --secrets-provider=awskms
```

-----

### COMANDO `delete` - Destruir Cluster

#### Flags Essenciais

| Flag | Tipo | Descrição |
| :--- | :--- | :--- |
| `--name` | string | Nome do cluster (mesmo usado em `create`) |
| `--state-bucket-name` | string | Bucket S3 do estado |
| `--secrets-provider` | string | Provider usado na criação (`awskms` ou `passphrase`) |
| `--kms-key-id` | string | ID da chave KMS (se usar `awskms`) |

#### Exemplos

##### 🔹 Deletar Cluster com KMS

```bash
@eks delete \
  --name=meu-cluster \
  --state-bucket-name=meu-bucket-state \
  --secrets-provider=awskms \
  --kms-key-id=alias/pulumi-secrets-meu-cluster
```

##### 🔹 Deletar Cluster com Passphrase

```bash
export PULUMI_CONFIG_PASSPHRASE='minha-senha-segura'

@eks delete \
  --name=meu-cluster \
  --state-bucket-name=meu-bucket-state \
  --secrets-provider=passphrase
```

-----

### COMANDO `cleanup` - Remover Recursos Auxiliares

> **⚠️ AVISO:** Operação IRREVERSÍVEL\! Use com cuidado.

#### Flags Essenciais

| Flag | Tipo | Padrão | Descrição |
| :--- | :--- | :--- | :--- |
| `--cluster-name` | string | - | Infere automaticamente nomes de recursos |
| `--state-bucket-name` | string | - | Bucket S3 específico |
| `--kms-key-alias` | string | - | Alias da chave KMS |
| `--preview` | bool | `false` | Mostra o que será deletado (seguro) |
| `--dry-run` | bool | `false` | Simula deleção (seguro) |
| `--auto-approve` | bool | `false` | **OBRIGATÓRIO** para executar deleções reais |

#### Exemplos

##### 🔹 Preview Seguro (Recomendado Primeiro)

```bash
@eks cleanup \
  --cluster-name=meu-cluster \
  --preview
```

**Output:**

```
📊 PREVIEW DE DELEÇÃO:

🪣 Bucket S3: meu-cluster-state
   - Objetos: 15
   - Versões: 3
   - Total a deletar: 18 itens

🔑 Chave KMS: alias/pulumi-secrets-meu-cluster
   - Será agendada para deleção em 7 dias
   - Custo atual: ~$1/mês

💰 Economia estimada após deleção: ~$1-5/mês
```

##### 🔹 Dry-Run (Testa sem Deletar)

```bash
@eks cleanup \
  --cluster-name=meu-cluster \
  --dry-run
```

##### 🔹 Executar Deleção Real

```bash
@eks cleanup \
  --cluster-name=meu-cluster \
  --auto-approve
```

##### 🔹 Cleanup Seletivo

```bash
# Deletar apenas bucket S3
@eks cleanup \
  --state-bucket-name=meu-bucket-antigo \
  --auto-approve

# Deletar apenas KMS
@eks cleanup \
  --kms-key-alias=pulumi-secrets-meu-cluster \
  --auto-approve
```

##### 🔹 Uso em CI/CD (Pipeline Seguro)

```bash
# 1. Preview em PR
@eks cleanup --cluster-name=staging --preview

# 2. Executar em merge
@eks cleanup \
  --cluster-name=staging \
  --region=us-east-1 \
  --auto-approve
```

-----

### COMANDO `kms-info` - Informações de Chave KMS

#### Flags

| Flag | Tipo | Descrição |
| :--- | :--- | :--- |
| `--cluster-name` | string | Nome do cluster (infere alias automaticamente) |
| `--kms-key-id` | string | Alias, KeyID ou ARN da chave |
| `--region` | string | Região AWS (padrão: `us-east-1`) |

#### Exemplos

##### 🔹 Por Nome do Cluster

```bash
@eks kms-info --cluster-name=prod-cluster
```

##### 🔹 Por Alias Específico

```bash
@eks kms-info --kms-key-id=alias/pulumi-secrets-prod-cluster
```

##### 🔹 Por ARN Completo

```bash
@eks kms-info \
  --kms-key-id=arn:aws:kms:us-east-1:123456789012:key/12345678-1234-1234-1234-123456789012
```

**Output:**

```yaml
🔑 INFORMAÇÕES DA CHAVE KMS:
============================================================
Alias:        alias/pulumi-secrets-prod-cluster
KeyID:        12345678-1234-1234-1234-123456789012
ARN:          arn:aws:kms:us-east-1:123456789012:key/12345...
Estado:       Enabled
Criado em:    2025-01-19T14:30:00Z
Gerenciado:   CUSTOMER

Tags:
  - ManagedBy: chatcli-eks
  - Purpose: pulumi-secrets
  - CreatedAt: 2025-01-19T14:30:00Z
============================================================
```

-----

## 🔐 GESTÃO DE SECRETS

### Opção 1: AWS KMS (Recomendado para Produção)

**Vantagens:**

* ✅ Sem necessidade de senhas manuais
* ✅ Auditoria automática via CloudTrail
* ✅ Rotação automática de chaves
* ✅ Integração nativa com IAM

**Custo:** \~$1/mês + $0.03 por 10k operações

```bash
# Criação automática de chave
@eks create \
  --name=meu-cluster \
  --state-bucket-name=meu-state \
  --secrets-provider=awskms

# Usar chave existente
@eks create \
  --name=meu-cluster \
  --state-bucket-name=meu-state \
  --secrets-provider=awskms \
  --kms-key-id=alias/minha-chave
```

### Opção 2: Passphrase (Desenvolvimento Local)

**Vantagens:**

* ✅ Sem custos AWS
* ✅ Simples para dev/test

**Desvantagens:**

* ⚠️ Precisa armazenar senha de forma segura
* ⚠️ Sem auditoria automática

<!-- end list -->

```bash
# Opção 1: Via variável de ambiente
export PULUMI_CONFIG_PASSPHRASE='minha-senha-super-segura'
@eks create \
  --name=dev-cluster \
  --state-bucket-name=dev-state \
  --secrets-provider=passphrase

# Opção 2: Via flag
@eks create \
  --name=dev-cluster \
  --state-bucket-name=dev-state \
  --secrets-provider=passphrase \
  --config-passphrase='minha-senha-super-segura'
```

-----

## 🎓 CASOS DE USO AVANÇADOS

### 1\. Migrar de Passphrase para KMS

```bash
# 1. Criar nova stack com KMS
@eks create \
  --name=prod-cluster-v2 \
  --state-bucket-name=prod-state-v2 \
  --secrets-provider=awskms

# 2. Deletar stack antiga
export PULUMI_CONFIG_PASSPHRASE='senha-antiga'
@eks delete \
  --name=prod-cluster \
  --state-bucket-name=prod-state \
  --secrets-provider=passphrase

# 3. Cleanup recursos antigos
@eks cleanup --cluster-name=prod-cluster --auto-approve
```

### 2\. Rotação de Chaves KMS

```bash
# Forçar criação de nova chave
@eks create \
  --name=meu-cluster \
  --state-bucket-name=meu-state \
  --secrets-provider=awskms \
  --kms-action=rotate

# Resultado: Cria alias/pulumi-secrets-meu-cluster-20250119-143000
```

### 3\. Blue-Green Deployment de Clusters

```bash
# 1. Criar cluster "green"
@eks create \
  --name=prod-green \
  --state-bucket-name=prod-green-state \
  --with-argocd \
  --argocd-domain=argocd-green.meusite.com \
  --secrets-provider=awskms

# 2. Validar aplicações no green

# 3. Deletar cluster "blue"
@eks delete \
  --name=prod-blue \
  --state-bucket-name=prod-blue-state \
  --secrets-provider=awskms \
  --kms-key-id=alias/pulumi-secrets-prod-blue

# 4. Cleanup
@eks cleanup --cluster-name=prod-blue --auto-approve
```

### 4\. Multi-Region Setup

```bash
# Região 1: us-east-1
@eks create \
  --name=prod-us-east \
  --region=us-east-1 \
  --state-bucket-name=prod-us-east-state \
  --secrets-provider=awskms

# Região 2: eu-west-1
@eks create \
  --name=prod-eu-west \
  --region=eu-west-1 \
  --state-bucket-name=prod-eu-west-state \
  --secrets-provider=awskms
```

-----

## 🐛 TROUBLESHOOTING

### Erro: “PULUMI_ACCESS_TOKEN must be set for login during non-interactive CLI sessions”

Causa: O backend padrão ficou como Pulumi Cloud, mas não há token disponível (em CI) nem login prévio no CLI.

Soluções:
- Opção A (sem Pulumi Cloud): use S3 ou backend local
  - Passe `--state-bucket-name` para usar S3 (recomendado em CI).
  - Ou rode sem bucket e deixe o plugin cair no backend local (file://).
- Opção B (Pulumi Cloud local): faça `pulumi login` uma vez no host (token ficará salvo).
- Opção C (Pulumi Cloud em CI): defina `PULUMI_ACCESS_TOKEN` no ambiente.

### Erro: "Stack incompatível com secrets provider"

**Causa:** Tentou usar secrets provider diferente do usado na criação.

**Solução:**

```bash
# Opção 1: Usar mesmo provider
@eks create \
  --name=meu-cluster \
  --secrets-provider=passphrase \
  --config-passphrase='senha-original'

# Opção 2: Criar nova stack
@eks create \
  --name=meu-cluster-v2 \
  --state-bucket-name=novo-bucket \
  --secrets-provider=awskms
```

### Erro: "Passphrase must be set"

**Solução:**

```bash
export PULUMI_CONFIG_PASSPHRASE='sua-senha'
# OU
@eks create --config-passphrase='sua-senha' ...
```

### Erro: "KMS Key not found"

**Solução:**

```bash
# Verificar chave existe
@eks kms-info --cluster-name=meu-cluster

# Criar nova se necessário
@eks create \
  --name=meu-cluster \
  --secrets-provider=awskms \
  --kms-action=rotate
```

### Certificado TLS não funciona

**Diagnóstico:**

```bash
# 1. Verificar certificado foi criado
kubectl get certificate -n cert-manager

# 2. Ver logs do cert-manager
kubectl logs -n cert-manager deploy/cert-manager -f

# 3. Verificar secret foi replicado
kubectl get secret wildcard-tls -n argocd
kubectl get secret wildcard-tls -n ingress-nginx
```

**Solução Comum:**

```bash
# Recriar certificado
kubectl delete certificate -n cert-manager wildcard-tls-cert
kubectl apply -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: wildcard-tls-cert
  namespace: cert-manager
spec:
  secretName: wildcard-tls
  issuerRef:
    name: letsencrypt-prod
    kind: ClusterIssuer
  dnsNames:
    - "*.meusite.com"
    - "meusite.com"
EOF
```

-----

## 💰 ESTIMATIVA DE CUSTOS

### Cluster Mínimo (Dev/QA)

* **EKS Control Plane:** $72/mês
* **2x t3.small (Spot):** \~$12/mês
* **NAT Gateway:** $32/mês
* **Total:** \~$116/mês

### Cluster Produção (sem Spot)

* **EKS Control Plane:** $72/mês
* **3x t3.medium (On-Demand):** \~$93/mês
* **3x NAT Gateway:** $96/mês
* **Load Balancers:** \~$30/mês
* **Total:** \~$291/mês

### Cluster Produção (com Spot)

* **EKS Control Plane:** $72/mês
* **3x t3.medium (Spot):** \~$28/mês
* **3x NAT Gateway:** $96/mês
* **Load Balancers:** \~$30/mês
* **Total:** \~$226/mês (economia de $65/mês)

### Recursos Auxiliares

* **S3 State Bucket:** \~$0.50/mês
* **KMS Key:** \~$1/mês
* **Total:** \~$1.75/mês

-----

## 📚 REFERÊNCIAS

* [Documentação Pulumi AWS](https://www.pulumi.com/docs/clouds/aws/)
* [EKS Best Practices](https://aws.github.io/aws-eks-best-practices/)
* [Cert-Manager Docs](https://cert-manager.io/docs/)
* [ArgoCD Documentation](https://argo-cd.readthedocs.io/)
* [AWS KMS Pricing](https://aws.amazon.com/kms/pricing/)

-----

## 🤝 CONTRIBUINDO

Encontrou um bug? Tem uma ideia? Abra uma issue ou PR\!

```bash
git clone [https://github.com/diillson/chatcli.git](https://github.com/diillson/chatcli.git)
cd chatcli/plugins-examples/chatcli-eks
# Faça suas alterações
git commit -m "feat: nova funcionalidade incrível"
git push origin feature/sua-feature
```

-----

## 📄 LICENÇA

MIT License - Veja arquivo `LICENSE` para detalhes.

-----

## 🎉 UAU\! VOCÊ CHEGOU ATÉ AQUI\!

Agora você tem TUDO que precisa para:

* ✅ Criar clusters EKS production-ready em minutos
* ✅ Configurar TLS automático com Let's Encrypt ou Google
* ✅ Implementar GitOps com ArgoCD
* ✅ Gerenciar secrets de forma segura com KMS
* ✅ Economizar custos com Spot Instances
* ✅ Automatizar DNS com External DNS
* ✅ Fazer cleanup completo sem deixar rastros

Dúvidas? Consulte os exemplos práticos acima ou abra uma issue\! 🚀