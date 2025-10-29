package e2e

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var chatcliBinary string

// TestMain é executado uma vez para compilar o binário
func TestMain(m *testing.M) {
	projectRoot, err := filepath.Abs("..")
	if err != nil {
		panic("could not find project root: " + err.Error())
	}

	binaryName := "chatcli_test_binary"
	chatcliBinary = filepath.Join(os.TempDir(), binaryName)

	// Compilar com ldflags separado em args para evitar erros de quote
	ldflags := "-ldflags=-X github.com/diillson/chatcli/version.Version=1.25.0"
	cmd := exec.Command("go", "build", ldflags, "-o", chatcliBinary, projectRoot)
	output, err := cmd.CombinedOutput()
	if err != nil {
		panic("failed to build chatcli binary: " + err.Error() + "\nOutput:\n" + string(output))
	}

	// Executar os testes
	exitCode := m.Run()

	os.Remove(chatcliBinary)
	os.Exit(exitCode)
}

func runChatCLI(t *testing.T, args []string, stdin string, env []string) (string, string) {
	cmd := exec.Command(chatcliBinary, args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}

	cmd.Env = append(os.Environ(), env...)

	err := cmd.Run()
	// Em testes E2E, um erro de saída não necessariamente é um erro de teste
	// Por exemplo, pedir ajuda causa exit code != 0 em alguns CLIs
	// Aqui, vamos apenas logar se houver erro, e as asserções verificarão o conteúdo.
	if err != nil {
		t.Logf("Command finished with error: %v. Stderr: %s", err, stderr.String())
	}

	return stdout.String(), stderr.String()
}

func TestE2E_OneShotMode(t *testing.T) {
	// 1. Criar um servidor mock da OpenAI que responde a múltiplos endpoints
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Header().Set("Content-Type", "application/json")

		var resp string
		// Verificar qual API está sendo chamada e responder adequadamente
		if strings.HasSuffix(r.URL.Path, "/v1/chat/completions") {
			resp = `{"choices": [{"message": {"role": "assistant", "content": "Response from mock ChatCompletions"}}],"usage":{"total_tokens":5}}`
		} else if strings.HasSuffix(r.URL.Path, "/v1/responses") {
			resp = `{"output_text": "Response from mock ResponsesAPI"}`
		} else {
			w.WriteHeader(http.StatusNotFound)
			resp = `{"error": "endpoint not mocked"}`
		}

		_, _ = w.Write([]byte(resp))
	}))
	defer server.Close()

	// 2. Configurar ambiente para que o binário use o servidor mock
	env := []string{
		"OPENAI_API_KEY=test-key",
		"LLM_PROVIDER=OPENAI",
		"OPENAI_API_URL=" + server.URL + "/v1/chat/completions",
		"OPENAI_RESPONSES_API_URL=" + server.URL + "/v1/responses",
		// Forçar o idioma inglês para consistência do teste
		"CHATCLI_LANG=en",
	}

	t.Run("Prompt via -p flag", func(t *testing.T) {
		args := []string{"-p", "hello world", "--no-anim"}
		stdout, stderr := runChatCLI(t, args, "", env)

		require.Empty(t, stderr, "Stderr should be empty on success")
		assert.Contains(t, stdout, "Response from mock")
	})

	t.Run("Prompt via stdin", func(t *testing.T) {
		args := []string{"--no-anim"}
		stdout, stderr := runChatCLI(t, args, "hello from stdin", env)

		require.Empty(t, stderr, "Stderr should be empty on success")
		assert.Contains(t, stdout, "Response from mock")
	})

	t.Run("Prompt via stdin and -p flag", func(t *testing.T) {
		args := []string{"-p", "prompt part", "--no-anim"}
		stdout, stderr := runChatCLI(t, args, "stdin part", env)

		require.Empty(t, stderr, "Stderr should be empty on success")
		assert.Contains(t, stdout, "Response from mock")
	})

	t.Run("Error on invalid provider", func(t *testing.T) {
		args := []string{"-p", "test", "--provider=INVALID"}
		_, stderr := runChatCLI(t, args, "", env)

		assert.Contains(t, stderr, "Error applying overrides")
	})
}

func TestE2E_VersionFlag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"tag_name": "v1.26.0"}`)) // Mock retorna uma versão mais nova
	}))
	defer server.Close()

	env := []string{
		"CHATCLI_LATEST_VERSION_URL=" + server.URL,
	}

	args := []string{"--version"}
	// Passamos o ambiente para a execução do comando.
	stdout, stderr := runChatCLI(t, args, "", env)

	require.Empty(t, stderr, "Stderr should be empty")
	// Agora o assert deve passar, pois o binário usará o mock e detectará a atualização.
	assert.Contains(t, stdout, "Disponível! Atualize para a versão mais recente.")
}
