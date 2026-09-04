/*
 * ChatCLI - AWS credential resolution and diagnostics for Bedrock.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * One place decides WHICH AWS identity every Bedrock surface talks with —
 * chat, agent/coder, model listing, embeddings and image generation — and
 * one place explains what to do when that identity cannot be resolved.
 *
 * The split used to be silent and lossy: the chat client read AWS_PROFILE
 * only, imagegen read BEDROCK_PROFILE first, and the registry factory read
 * neither from the .env. So a BEDROCK_PROFILE documented in /config (it is
 * listed in reloadableEnvVars) selected the account for images and was
 * ignored for inference, and a profile that lived only in the environment
 * file was invisible to any surface that did not export it to the process.
 */
package bedrock

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
)

// ProfileEnvKeys are the variables that select the AWS profile, in
// precedence order. BEDROCK_PROFILE scopes a profile to ChatCLI without
// touching AWS_PROFILE for the rest of the shell.
var ProfileEnvKeys = []string{"BEDROCK_PROFILE", "AWS_PROFILE"}

// RegionEnvKeys are the variables that select the region, in precedence order.
var RegionEnvKeys = []string{"BEDROCK_REGION", "AWS_REGION", "AWS_DEFAULT_REGION"}

// envOrConfig reads a variable from the process environment first and from
// the loaded environment file second. The second source matters because
// godotenv only exports to the process when ChatCLI itself loaded the file;
// values reaching ChatCLI through config.Global (managed policy, a file read
// after boot) would otherwise be invisible to the AWS SDK.
func envOrConfig(keys ...string) (value, key string) {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v, k
		}
	}
	if config.Global != nil {
		for _, k := range keys {
			if v := strings.TrimSpace(config.Global.GetString(k)); v != "" {
				return v, k
			}
		}
	}
	return "", ""
}

// ResolveProfile returns the AWS profile every Bedrock surface must use and
// the variable it came from ("" when no profile is configured, which lets
// the SDK fall back to its own default profile).
func ResolveProfile() (profile, source string) {
	return envOrConfig(ProfileEnvKeys...)
}

// ResolveRegion returns the configured region and its source, without
// applying a default — callers decide their own fallback.
func ResolveRegion() (region, source string) {
	return envOrConfig(RegionEnvKeys...)
}

// awsConfigPath returns the shared config file path, honoring AWS_CONFIG_FILE.
func awsConfigPath() string {
	if v := strings.TrimSpace(os.Getenv("AWS_CONFIG_FILE")); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".aws", "config")
}

// awsCredentialsPath returns the shared credentials file path, honoring
// AWS_SHARED_CREDENTIALS_FILE.
func awsCredentialsPath() string {
	if v := strings.TrimSpace(os.Getenv("AWS_SHARED_CREDENTIALS_FILE")); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".aws", "credentials")
}

// awsCacheDirs are the token caches written by an interactive AWS login:
// ~/.aws/sso/cache for `aws sso login` and ~/.aws/login/cache for the newer
// `aws login` flow (AWS CLI v2 "login_session" profiles, supported by the
// SDK since aws-sdk-go-v2/config v1.33). A profile that authenticates that
// way carries no key material in ~/.aws/credentials, so without looking
// here ChatCLI concluded there were no AWS credentials at all and never
// registered the Bedrock provider.
func awsCacheDirs() []string {
	if v := strings.TrimSpace(os.Getenv("AWS_LOGIN_CACHE_DIRECTORY")); v != "" {
		return []string{v}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, ".aws", "sso", "cache"),
		filepath.Join(home, ".aws", "login", "cache"),
	}
}

// HasCachedLoginToken reports whether an interactive AWS login left a token
// cache behind (SSO or the newer `aws login`). An expired token still counts:
// the SDK reports that precisely at call time, which is a far better error
// than pretending the provider does not exist.
func HasCachedLoginToken() bool {
	for _, dir := range awsCacheDirs() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
				return true
			}
		}
	}
	return false
}

// credentialErrorMarkers are the SDK strings that mean "the credential chain
// produced nothing usable" — as opposed to a genuine Bedrock API error.
var credentialErrorMarkers = []string{
	"failed to refresh cached credentials",
	"no ec2 imds role found",
	"ec2imds",
	"get credentials:",
	"get identity: get credentials",
	"failed to get shared config profile",
	"failed to load shared config",
	"failed to retrieve credentials",
	"no valid credential sources",
	"sso session",
	"sso token",
	"token has expired",
	"expiredtoken",
	"invalidgrantexception",
	"invalidclienttokenid",
	"unrecognizedclientexception",
	"security token included in the request is invalid",
	"resolve aws credentials",
}

// IsCredentialError reports whether err is a credential-chain failure.
// String matching is the pragmatic option: the SDK fmt-joins these chains
// without stable sentinel types across credential providers.
func IsCredentialError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	for _, marker := range credentialErrorMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// ExplainCredentialError annotates an AWS credential failure with the state
// that actually explains it: the profile in use and where it came from, and
// the environment file in effect.
//
// Raw, the SDK says "no EC2 IMDS role found" — which reads like an EC2
// problem and says nothing about the real cause on a developer machine: the
// profile never reached the process. That is the normal outcome when an
// editor or MCP client spawns `chatcli acp`/`chatcli mcp-server` without the
// user's shell environment, so the message names that case explicitly.
//
// Non-credential errors are returned untouched.
func ExplainCredentialError(err error) error {
	if !IsCredentialError(err) {
		return err
	}
	profile, source := ResolveProfile()
	profileLabel := profile
	if profile == "" {
		profileLabel = i18n.T("llm.bedrock.creds_profile_none")
		source = "-"
	}
	dotenv := config.ActiveDotenv()
	dotenvLabel := dotenv.Path
	if !dotenv.Exists {
		dotenvLabel = i18n.T("llm.bedrock.creds_dotenv_missing", dotenv.Path)
	}
	loginHint := "aws sso login"
	if profile != "" {
		loginHint = "aws sso login --profile " + profile
	}
	// Wrapped, never replaced: errors.As/Is walks this chain (payload-cap
	// recovery and the retry classifier both inspect it).
	return fmt.Errorf("%s: %w", i18n.T("llm.bedrock.creds_unresolved",
		profileLabel, source, dotenvLabel, string(dotenv.Origin), loginHint), err)
}

// CredentialsAvailable reports whether there is a credible signal that AWS
// credentials are usable, so ChatCLI can register the Bedrock provider
// without the AWS SDK falling through to IMDS (169.254.169.254) and hanging
// with a confusing timeout on a machine that is not an EC2 instance.
//
// The mere existence of ~/.aws/config is deliberately NOT a signal (it may
// hold only region/output metadata), and neither is an empty
// ~/.aws/credentials.
//
// Accepted signals (any one is enough):
//   - Static credentials in the environment (AWS_ACCESS_KEY_ID)
//   - A profile selection (BEDROCK_PROFILE / AWS_PROFILE), from the process
//     environment or the loaded .env — SSO, assume-role, credential_process
//   - A Bedrock API key (AWS_BEARER_TOKEN_BEDROCK), used by the Mantle surface
//   - Web identity / container roles (EKS / ECS)
//   - A shared credentials file with a non-empty aws_access_key_id
//   - A shared config declaring a usable profile: SSO (sso_start_url /
//     sso_session), the newer `aws login` (login_session), assume-role
//     (role_arn) or credential_process
//   - A cached login token (~/.aws/sso/cache or ~/.aws/login/cache)
func CredentialsAvailable() bool {
	for _, key := range []string{
		"AWS_ACCESS_KEY_ID",
		"AWS_BEARER_TOKEN_BEDROCK",
		"AWS_WEB_IDENTITY_TOKEN_FILE",
		"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI",
		"AWS_CONTAINER_CREDENTIALS_FULL_URI",
	} {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return true
		}
	}
	// Profile from the environment OR from the loaded .env: godotenv exports
	// to the process, but a value that only reached config.Global (managed
	// policy) must count just the same.
	if profile, _ := ResolveProfile(); profile != "" {
		return true
	}
	if credentialsFileHasKey(awsCredentialsPath()) {
		return true
	}
	if configFileHasUsableProfile(awsConfigPath()) {
		return true
	}
	return HasCachedLoginToken()
}

// credentialsFileHasKey returns true only if the AWS shared credentials file
// contains at least one non-empty aws_access_key_id entry. An empty or
// region-only file is not enough to activate Bedrock.
func credentialsFileHasKey(path string) bool {
	if path == "" {
		return false
	}
	data, err := os.ReadFile(filepath.Clean(path)) // #nosec G304 — path from AWS_SHARED_CREDENTIALS_FILE or os.UserHomeDir()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		if strings.HasPrefix(strings.ToLower(trimmed), "aws_access_key_id") {
			if idx := strings.Index(trimmed, "="); idx >= 0 {
				if strings.TrimSpace(trimmed[idx+1:]) != "" {
					return true
				}
			}
		}
	}
	return false
}

// usableProfileKeys are the shared-config keys that can produce credentials
// without IMDS. login_session is the newer `aws login` flow: a profile that
// carries it has no key material anywhere else, so omitting it here made
// ChatCLI declare "no AWS credentials" for a user who was, in fact, logged in.
var usableProfileKeys = []string{
	"sso_start_url",  // legacy SSO profile
	"sso_session",    // new-style SSO (aws sso login)
	"sso_account_id", // ditto
	"login_session",  // aws login (AWS CLI v2 sign-in)
	"role_arn",       // assume-role profile (source_profile / web identity)
	"credential_process",
}

// configFileHasUsableProfile returns true if the shared config declares at
// least one profile that can produce credentials without IMDS. A config that
// only sets region/output does NOT count.
func configFileHasUsableProfile(path string) bool {
	if path == "" {
		return false
	}
	data, err := os.ReadFile(filepath.Clean(path)) // #nosec G304 — path from AWS_CONFIG_FILE or os.UserHomeDir()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		lower := strings.ToLower(trimmed)
		for _, key := range usableProfileKeys {
			if strings.HasPrefix(lower, key) {
				if idx := strings.Index(trimmed, "="); idx >= 0 &&
					strings.TrimSpace(trimmed[idx+1:]) != "" {
					return true
				}
			}
		}
	}
	return false
}
