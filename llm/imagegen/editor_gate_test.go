package imagegen

import (
	"testing"

	"go.uber.org/zap"
)

// TestAsEditor_GatesGenerationOnlyBackends proves the editing capability is
// declared per backend, as the factory builds them: OpenAI and self-hosted
// edit in OpenAI's multipart shape, xAI edits in its own JSON shape, and Z.AI
// (no edit endpoint) is reported as non-editing so @image edit fails clearly
// instead of firing a doomed request. SD WebUI (img2img) stays editable.
func TestAsEditor_GatesGenerationOnlyBackends(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("XAI_API_KEY", "k")
	t.Setenv("ZAI_API_KEY", "k")
	t.Setenv("CHATCLI_IMAGE_URL_EDITS", "")
	log := zap.NewNop()

	cases := []struct {
		name   string
		p      Provider
		wantOK bool
	}{
		{"openai", cloudOrNull(openAIBaseURL, "k", "", "openai", log, ""), true},
		{"selfhosted", selfHostedOrNull("http://localhost:8080/v1", "", log), true},
		{"xai", xaiOrNull("", log, ""), true},
		{"zai", zaiOrNull("", log, ""), false},
		{"minimax", minimaxOrNull("", log, ""), false},
	}
	for _, c := range cases {
		if _, ok := AsEditor(c.p); ok != c.wantOK {
			t.Errorf("%s: AsEditor ok=%v, want %v", c.name, ok, c.wantOK)
		}
	}

	// SD WebUI does not implement editCapable → assumed editing-capable.
	sd, err := NewAutomatic1111("http://localhost:7860", 25, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := AsEditor(sd); !ok {
		t.Error("SD WebUI (img2img) should be reported as editing-capable")
	}
}
