package cli

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/diillson/chatcli/models"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

// O loop do agent/coder ignorava o override de sessão do /max-tokens
// (currentMaxTokens fixo em 0 = provider default). adoptSessionMaxTokens é a
// reconciliação por turno: adota o override sempre que o usuário o altera,
// sem clobberar a escalation de truncação quando o override não mudou.
func TestAdoptSessionMaxTokens(t *testing.T) {
	cases := []struct {
		name        string
		current     int
		lastAdopted int
		override    int
		wantCurrent int
		wantAdopted int
	}{
		{
			name:        "no override keeps provider default",
			current:     0,
			lastAdopted: 0,
			override:    0,
			wantCurrent: 0,
			wantAdopted: 0,
		},
		{
			name:        "first override is adopted",
			current:     0,
			lastAdopted: 0,
			override:    32000,
			wantCurrent: 32000,
			wantAdopted: 32000,
		},
		{
			name:        "raised override is adopted",
			current:     32000,
			lastAdopted: 32000,
			override:    64000,
			wantCurrent: 64000,
			wantAdopted: 64000,
		},
		{
			name:        "lowered override is adopted",
			current:     64000,
			lastAdopted: 64000,
			override:    16000,
			wantCurrent: 16000,
			wantAdopted: 16000,
		},
		{
			name:        "unchanged override preserves truncation escalation",
			current:     96000, // escalation raised beyond the 64000 override
			lastAdopted: 64000,
			override:    64000,
			wantCurrent: 96000,
			wantAdopted: 64000,
		},
		{
			name:        "override change wins over prior escalation",
			current:     96000,
			lastAdopted: 64000,
			override:    128000,
			wantCurrent: 128000,
			wantAdopted: 128000,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotCurrent, gotAdopted := adoptSessionMaxTokens(tc.current, tc.lastAdopted, tc.override)
			assert.Equal(t, tc.wantCurrent, gotCurrent, "current")
			assert.Equal(t, tc.wantAdopted, gotAdopted, "lastAdopted")
		})
	}
}

// maxTokensCaptureFake registra o maxTokens recebido pelo SendPrompt para
// provar que o override de sessão chega ao provider.
type maxTokensCaptureFake struct {
	gotMaxTokens int
	err          error
}

func (f *maxTokensCaptureFake) GetModelName() string { return "fake" }
func (f *maxTokensCaptureFake) SendPrompt(_ context.Context, _ string, _ []models.Message, maxTokens int) (string, error) {
	f.gotMaxTokens = maxTokens
	return "ok", f.err
}

// sendOutputToAI é caminho de chat: o override de sessão do /max-tokens
// precisa chegar ao SendPrompt em vez do 0 fixo que ignorava o ajuste.
func TestSendOutputToAI_HonorsSessionMaxTokens(t *testing.T) {
	fake := &maxTokensCaptureFake{err: errors.New("synthetic: stop before rendering")}
	c := &ChatCLI{
		animation:     NewAnimationManager(),
		logger:        zap.NewNop(),
		Client:        fake,
		UserMaxTokens: 32000,
	}
	c.animation.SetSuppressed(true)

	c.sendOutputToAI("stdout sample", "extra context")

	assert.Equal(t, 32000, fake.gotMaxTokens,
		"session /max-tokens override must reach SendPrompt")
	// O prompt localizado entra no histórico como mensagem de usuário.
	assert.Len(t, c.history, 1)
	assert.Equal(t, "user", c.history[0].Role)
	assert.Contains(t, c.history[0].Content, "stdout sample")
}

// effectiveMaxTokensDisplay marca quando o número exibido vem do override de
// sessão, para o /config nunca ler como default estático.
func TestEffectiveMaxTokensDisplay(t *testing.T) {
	plain := &ChatCLI{Provider: "OPENAI", Model: "unknown-model"}
	assert.Equal(t, fmt.Sprintf("%d", plain.getMaxTokensForCurrentLLM()),
		plain.effectiveMaxTokensDisplay(),
		"without an override the display is the bare effective number")

	withOverride := &ChatCLI{Provider: "OPENAI", Model: "unknown-model", UserMaxTokens: 64000}
	got := withOverride.effectiveMaxTokensDisplay()
	// A camada i18n aplica separador de milhar ao %d ("64,000"), então a
	// asserção usa o prefixo do número e a annotation, estáveis nos locales.
	assert.Contains(t, got, "64", "override value must be displayed")
	assert.Contains(t, got, "/max-tokens",
		"override display must carry the session-override annotation")
}
