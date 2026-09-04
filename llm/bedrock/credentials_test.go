package bedrock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/diillson/chatcli/config"
	"go.uber.org/zap"
)

// isolateAWSEnv clears every variable that can decide the AWS identity and
// points HOME at a scratch directory, so a test never inherits the machine's
// real credentials.
func isolateAWSEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}
	for _, k := range []string{
		"BEDROCK_PROFILE", "AWS_PROFILE", "BEDROCK_REGION", "AWS_REGION", "AWS_DEFAULT_REGION",
		"AWS_ACCESS_KEY_ID", "AWS_BEARER_TOKEN_BEDROCK", "AWS_WEB_IDENTITY_TOKEN_FILE",
		"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "AWS_CONTAINER_CREDENTIALS_FULL_URI",
		"AWS_CONFIG_FILE", "AWS_SHARED_CREDENTIALS_FILE", "AWS_LOGIN_CACHE_DIRECTORY",
	} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	config.ResetGlobalForTest(zap.NewNop())
	return home
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestResolveProfile_Precedence(t *testing.T) {
	isolateAWSEnv(t)

	if p, src := ResolveProfile(); p != "" || src != "" {
		t.Fatalf("no profile configured should resolve empty, got %q/%q", p, src)
	}

	// .env-only value (never exported to the process) must still be seen.
	config.Global.Set("AWS_PROFILE", "from-dotenv")
	if p, src := ResolveProfile(); p != "from-dotenv" || src != "AWS_PROFILE" {
		t.Fatalf("dotenv profile: %q/%q", p, src)
	}

	// The process environment outranks the file.
	t.Setenv("AWS_PROFILE", "from-env")
	if p, _ := ResolveProfile(); p != "from-env" {
		t.Fatalf("env must outrank the dotenv, got %q", p)
	}

	// BEDROCK_PROFILE scopes a profile to ChatCLI and outranks AWS_PROFILE —
	// it was honored by image generation and ignored by inference before.
	t.Setenv("BEDROCK_PROFILE", "bedrock-only")
	if p, src := ResolveProfile(); p != "bedrock-only" || src != "BEDROCK_PROFILE" {
		t.Fatalf("BEDROCK_PROFILE must win: %q/%q", p, src)
	}
}

func TestResolveRegion_Precedence(t *testing.T) {
	isolateAWSEnv(t)
	config.Global.Set("AWS_REGION", "eu-west-1")
	if r, _ := ResolveRegion(); r != "eu-west-1" {
		t.Fatalf("dotenv region: %q", r)
	}
	t.Setenv("AWS_DEFAULT_REGION", "us-west-2")
	if r, _ := ResolveRegion(); r != "us-west-2" {
		t.Fatalf("process env must win: %q", r)
	}
	t.Setenv("BEDROCK_REGION", "sa-east-1")
	if r, src := ResolveRegion(); r != "sa-east-1" || src != "BEDROCK_REGION" {
		t.Fatalf("BEDROCK_REGION must win: %q/%q", r, src)
	}
}

func TestCredentialsAvailable_Signals(t *testing.T) {
	home := isolateAWSEnv(t)
	if CredentialsAvailable() {
		t.Fatal("an empty machine must not advertise Bedrock")
	}

	// A config file with only region/output is NOT a credential.
	cfgPath := filepath.Join(home, ".aws", "config")
	writeFile(t, cfgPath, "[default]\nregion = us-east-1\noutput = json\n")
	if CredentialsAvailable() {
		t.Fatal("region-only config must not count as a credential")
	}

	// An empty credentials file is not one either.
	writeFile(t, filepath.Join(home, ".aws", "credentials"), "[default]\n")
	if CredentialsAvailable() {
		t.Fatal("empty credentials file must not count")
	}

	// `aws login` profile: no key material anywhere, only login_session.
	writeFile(t, cfgPath, "[default]\nregion = us-east-1\nlogin_session = arn:aws:iam::123456789012:root\n")
	if !CredentialsAvailable() {
		t.Fatal("an `aws login` (login_session) profile must register Bedrock")
	}
}

func TestCredentialsAvailable_LoginTokenCache(t *testing.T) {
	home := isolateAWSEnv(t)
	writeFile(t, filepath.Join(home, ".aws", "login", "cache", "token.json"), "{}")
	if !CredentialsAvailable() {
		t.Fatal("a cached `aws login` token must register Bedrock")
	}
}

func TestCredentialsAvailable_DotenvProfile(t *testing.T) {
	isolateAWSEnv(t)
	config.Global.Set("BEDROCK_PROFILE", "work")
	if !CredentialsAvailable() {
		t.Fatal("a profile that lives only in the .env must register Bedrock")
	}
}

func TestCredentialsAvailable_HonorsAWSConfigFileOverride(t *testing.T) {
	isolateAWSEnv(t)
	custom := filepath.Join(t.TempDir(), "aws.conf")
	writeFile(t, custom, "[profile work]\nsso_session = corp\n")
	t.Setenv("AWS_CONFIG_FILE", custom)
	if !CredentialsAvailable() {
		t.Fatal("AWS_CONFIG_FILE must be honored")
	}
}

func TestIsCredentialError(t *testing.T) {
	imds := errors.New(`operation error Bedrock Runtime: InvokeModel, get identity: get credentials: ` +
		`failed to refresh cached credentials, no EC2 IMDS role found`)
	if !IsCredentialError(imds) {
		t.Fatal("the IMDS-exhaustion shape is the signature failure of a missing profile")
	}
	if IsCredentialError(errors.New("ValidationException: model not supported")) {
		t.Fatal("a genuine API error must not be classified as a credential failure")
	}
	if IsCredentialError(nil) {
		t.Fatal("nil is not an error")
	}
}

func TestExplainCredentialError_AnnotatesAndPreservesChain(t *testing.T) {
	isolateAWSEnv(t)
	t.Setenv("AWS_PROFILE", "work")

	sentinel := errors.New("failed to refresh cached credentials, no EC2 IMDS role found")
	wrapped := fmt.Errorf("operation error Bedrock Runtime: %w", sentinel)

	out := ExplainCredentialError(wrapped)
	if !errors.Is(out, sentinel) {
		t.Fatal("the original chain must survive for errors.Is/As")
	}
	msg := out.Error()
	for _, want := range []string{"work", "aws sso login --profile work"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message must name the profile and the fix; got %q", msg)
		}
	}

	// Non-credential errors are returned untouched.
	other := errors.New("ValidationException")
	if got := ExplainCredentialError(other); got != other {
		t.Fatalf("non-credential errors must pass through unchanged, got %v", got)
	}
	if ExplainCredentialError(nil) != nil {
		t.Fatal("nil must stay nil")
	}
}
