package imagegen

import (
	"testing"

	"go.uber.org/zap"
)

// TestAsEditor_GatesGenerationOnlyBackends proves the editing capability is
// gated per-endpoint: OpenAI and self-hosted expose edits; xAI/Z.AI (which
// share the OpenAI-compatible client but have no /images/edits) are reported
// as non-editing so @image edit degrades cleanly instead of firing a doomed
// request. SD WebUI (img2img) stays editable.
func TestAsEditor_GatesGenerationOnlyBackends(t *testing.T) {
	mk := func(label string, canEdit bool) *OpenAICompatible {
		p, err := NewOpenAICompatible("https://x.example", "k", "m", label, zap.NewNop())
		if err != nil {
			t.Fatal(err)
		}
		p.canEdit = canEdit
		return p
	}

	cases := []struct {
		name   string
		p      Provider
		wantOK bool
	}{
		{"openai", mk("openai", true), true},
		{"selfhosted", mk("selfhosted", true), true},
		{"xai", mk("xai", false), false},
		{"zai", mk("zai", false), false},
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
