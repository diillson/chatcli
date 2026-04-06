/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"os"
	"strings"
)

// EnvRedactMode controls how environment variable redaction works.
type EnvRedactMode string

const (
	// EnvRedactStrict uses an allowlist — only explicitly safe vars are shown.
	EnvRedactStrict EnvRedactMode = "strict"
	// EnvRedactPermissive redacts known sensitive vars but shows the rest.
	EnvRedactPermissive EnvRedactMode = "permissive"
)

// EnvRedactor sanitizes environment variables before sending to LLM.
type EnvRedactor struct {
	mode              EnvRedactMode
	sensitiveExact    map[string]bool
	sensitivePatterns []string
	valuePatterns     []string
	safeVars          map[string]bool // for strict mode
	extraPatterns     []string
}

// NewEnvRedactor creates a redactor configured from environment.
// CHATCLI_ENV_REDACT_MODE: strict or permissive (default: permissive)
// CHATCLI_REDACT_PATTERNS: comma-separated additional patterns
func NewEnvRedactor() *EnvRedactor {
	mode := EnvRedactPermissive
	if strings.EqualFold(os.Getenv("CHATCLI_ENV_REDACT_MODE"), "strict") {
		mode = EnvRedactStrict
	}

	r := &EnvRedactor{
		mode: mode,
		sensitiveExact: map[string]bool{
			// AWS
			"AWS_SECRET_ACCESS_KEY": true, "AWS_SESSION_TOKEN": true,
			"AWS_SECURITY_TOKEN": true,
			// Cloud providers
			"GOOGLE_API_KEY": true, "GOOGLE_APPLICATION_CREDENTIALS": true,
			"AZURE_CLIENT_SECRET": true, "AZURE_TENANT_ID": true,
			"AZURE_SUBSCRIPTION_ID": true,
			// AI/LLM providers
			"OPENAI_API_KEY": true, "ANTHROPIC_API_KEY": true,
			"GOOGLE_AI_API_KEY": true, "GEMINI_API_KEY": true,
			"XAI_API_KEY": true, "GROK_API_KEY": true,
			"STACKSPOT_CLIENT_SECRET": true, "STACKSPOT_CLIENT_ID": true,
			"ZHIPU_API_KEY": true, "MINIMAX_API_KEY": true,
			"COHERE_API_KEY": true, "DEEPSEEK_API_KEY": true,
			"MISTRAL_API_KEY": true, "GROQ_API_KEY": true,
			// GitHub/Git
			"GITHUB_TOKEN": true, "GH_TOKEN": true,
			"GITHUB_PAT": true, "GIT_TOKEN": true,
			"GITLAB_TOKEN": true, "GITLAB_PAT": true,
			"BITBUCKET_TOKEN": true,
			// Databases
			"DATABASE_URL": true, "DB_PASSWORD": true,
			"POSTGRES_PASSWORD": true, "MYSQL_PASSWORD": true,
			"MYSQL_ROOT_PASSWORD": true, "REDIS_PASSWORD": true,
			"REDIS_URL": true, "MONGO_URI": true,
			"MONGODB_URI": true, "MONGO_URL": true,
			"CASSANDRA_PASSWORD": true,
			// Messaging
			"SLACK_TOKEN": true, "SLACK_BOT_TOKEN": true,
			"SLACK_WEBHOOK_URL": true,
			"DISCORD_TOKEN":     true, "DISCORD_BOT_TOKEN": true,
			"TELEGRAM_BOT_TOKEN": true,
			// Payment/SaaS
			"STRIPE_SECRET_KEY": true, "STRIPE_WEBHOOK_SECRET": true,
			"TWILIO_AUTH_TOKEN": true, "TWILIO_ACCOUNT_SID": true,
			"SENDGRID_API_KEY": true, "MAILGUN_API_KEY": true,
			// Auth/Crypto
			"JWT_SECRET": true, "JWT_PRIVATE_KEY": true,
			"SESSION_SECRET": true, "COOKIE_SECRET": true,
			"ENCRYPTION_KEY": true, "PRIVATE_KEY": true,
			"SECRET_KEY": true, "SIGNING_KEY": true,
			"MASTER_KEY": true,
			// Docker/Registry
			"DOCKER_PASSWORD": true, "DOCKER_TOKEN": true,
			"REGISTRY_PASSWORD": true,
			// ChatCLI specific
			"CHATCLI_JWT_SECRET": true, "CHATCLI_ENCRYPTION_KEY": true,
			"CHATCLI_SERVER_TOKEN": true,
		},
		sensitivePatterns: []string{
			"_KEY", "_SECRET", "_TOKEN", "_PASSWORD",
			"_CREDENTIAL", "_API_KEY", "_AUTH",
			"_PRIVATE_KEY", "_ACCESS_KEY", "_SECRET_KEY",
			"_PASS", "_PWD", "_APIKEY",
		},
		valuePatterns: []string{
			"sk-",         // OpenAI
			"ghp_",        // GitHub PAT
			"gho_",        // GitHub OAuth
			"ghs_",        // GitHub App
			"github_pat_", // GitHub fine-grained
			"xoxb-",       // Slack bot
			"xoxp-",       // Slack user
			"xoxa-",       // Slack app
			"AKIA",        // AWS access key
			"eyJ",         // JWT/base64 encoded
			"Bearer ",     // auth header
			"Basic ",      // basic auth
			"sk_live_",    // Stripe live
			"sk_test_",    // Stripe test
			"rk_live_",    // Stripe restricted
			"whsec_",      // Stripe webhook
			"SG.",         // SendGrid
		},
		safeVars: map[string]bool{
			// Shell/System
			"HOME": true, "USER": true, "SHELL": true,
			"TERM": true, "LANG": true, "LC_ALL": true,
			"PATH": true, "PWD": true, "OLDPWD": true,
			"EDITOR": true, "VISUAL": true, "PAGER": true,
			"HOSTNAME": true, "LOGNAME": true, "TZ": true,
			"DISPLAY": true, "XDG_RUNTIME_DIR": true,
			// Development
			"GOPATH": true, "GOROOT": true, "GOBIN": true,
			"GOPROXY": true, "GONOSUMCHECK": true,
			"NODE_ENV": true, "NODE_PATH": true,
			"JAVA_HOME": true, "MAVEN_HOME": true,
			"PYTHONPATH": true, "VIRTUAL_ENV": true,
			"CARGO_HOME": true, "RUSTUP_HOME": true,
			"RUBY_VERSION": true, "GEM_HOME": true,
			"NVM_DIR": true, "VOLTA_HOME": true,
			// Build
			"CI": true, "CI_COMMIT_SHA": true,
			"CI_PIPELINE_ID": true, "BUILD_NUMBER": true,
			"GITHUB_ACTIONS": true, "GITHUB_REPOSITORY": true,
			"GITHUB_REF": true, "GITHUB_SHA": true,
			// Config
			"LLM_PROVIDER": true, "HTTP_PROXY": true,
			"HTTPS_PROXY": true, "NO_PROXY": true,
			"KUBECONFIG": true, "DOCKER_HOST": true,
		},
	}

	// Add custom patterns from env
	if extra := os.Getenv("CHATCLI_REDACT_PATTERNS"); extra != "" {
		for _, p := range strings.Split(extra, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				r.extraPatterns = append(r.extraPatterns, p)
			}
		}
	}

	return r
}

// RedactEnv sanitizes a map of environment variables, replacing sensitive values with [REDACTED].
func (r *EnvRedactor) RedactEnv(envVars map[string]string) map[string]string {
	result := make(map[string]string, len(envVars))

	for key, value := range envVars {
		if r.isSensitive(key, value) {
			result[key] = "[REDACTED]"
		} else {
			result[key] = value
		}
	}

	return result
}

// RedactEnvSlice processes os.Environ()-style KEY=VALUE strings.
func (r *EnvRedactor) RedactEnvSlice(environ []string) map[string]string {
	envMap := make(map[string]string, len(environ))
	for _, e := range environ {
		if idx := strings.IndexByte(e, '='); idx > 0 {
			envMap[e[:idx]] = e[idx+1:]
		}
	}
	return r.RedactEnv(envMap)
}

func (r *EnvRedactor) isSensitive(key, value string) bool {
	// In strict mode, only show explicitly safe vars
	if r.mode == EnvRedactStrict {
		return !r.safeVars[key]
	}

	// Permissive mode: check against known sensitive patterns
	upperKey := strings.ToUpper(key)

	// Exact match
	if r.sensitiveExact[upperKey] {
		return true
	}

	// Suffix pattern match
	for _, pattern := range r.sensitivePatterns {
		if strings.HasSuffix(upperKey, pattern) {
			return true
		}
	}

	// Extra custom patterns
	for _, pattern := range r.extraPatterns {
		if strings.Contains(upperKey, strings.ToUpper(pattern)) {
			return true
		}
	}

	// Value-based detection (check prefixes that indicate secrets)
	for _, prefix := range r.valuePatterns {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}

	// Long hex strings (likely keys/tokens) — 32+ chars of hex
	if len(value) >= 32 && isHexString(value) {
		return true
	}

	return false
}

func isHexString(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
