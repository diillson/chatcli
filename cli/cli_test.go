package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/diillson/chatcli/version"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

// stripANSI remove códigos ANSI de uma string
func stripANSI(s string) string {
	re := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	return re.ReplaceAllString(s, "")
}

// normalizeSpaces remove espaços extras para asserções flexíveis
func normalizeSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func TestHandleVersionCommand(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	cliInstance := &ChatCLI{logger: logger}
	handler := NewCommandHandler(cliInstance)

	originalFetchImpl := version.FetchLatestReleaseImpl
	originalBuildImpl := version.GetBuildInfoImpl

	version.GetBuildInfoImpl = func() (string, string, string) {
		return "1.25.0", "abc1234", "2024-09-15"
	}
	defer func() {
		version.FetchLatestReleaseImpl = originalFetchImpl
		version.GetBuildInfoImpl = originalBuildImpl
	}()

	// mockLatest alimenta o seam de fetch; "precisa atualizar" é decidido
	// pela lógica REAL de comparação contra a versão mockada 1.25.0.
	testCases := []struct {
		name       string
		mockLatest string
		mockErr    error
		expectOut  string
	}{
		{"Update available", "1.26.0", nil, "v1.26.0 available — run /update"},
		{"No update", "1.25.0", nil, "You're already on the latest version."},
		{"With error", "", errors.New("network error"), "Couldn't check for updates: network error"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			version.FetchLatestReleaseImpl = func(ctx context.Context) (version.ReleaseInfo, error) {
				if tc.mockErr != nil {
					return version.ReleaseInfo{}, tc.mockErr
				}
				return version.ReleaseInfo{TagName: "v" + tc.mockLatest}, nil
			}

			oldStdout := os.Stdout
			r, w, _ := os.Pipe()
			os.Stdout = w
			defer func() { os.Stdout = oldStdout }()

			handler.handleVersionCommand(context.Background())

			w.Close()
			out, _ := io.ReadAll(r)

			cleanOut := stripANSI(string(out))
			normalized := normalizeSpaces(cleanOut)

			assert.Contains(t, normalized, tc.expectOut)
		})
	}
}
