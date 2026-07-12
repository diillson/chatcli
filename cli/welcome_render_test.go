package cli

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/diillson/chatcli/ui/theme"
)

// TestPrintWelcomeScreenRendersCards drives the full welcome render (logo,
// tip box, model-less card, footer) and asserts the responsive frame stays
// intact — covering the theme-aware border and anchor paths.
func TestPrintWelcomeScreenRendersCards(t *testing.T) {
	theme.SetProfile(theme.ProfileNoTTY)
	t.Cleanup(func() { theme.SetProfile(theme.DetectProfile()) })

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	c := &ChatCLI{}
	c.PrintWelcomeScreen()
	_ = w.Close()
	raw, _ := io.ReadAll(r)
	out := string(raw)

	for _, want := range []string{"──", "◆"} {
		if !strings.Contains(out, want) {
			t.Errorf("welcome output missing %q", want)
		}
	}
	for _, frame := range []string{"╭", "╰", "│"} {
		if strings.Contains(out, frame) {
			t.Errorf("sóbrio welcome must not draw frame glyph %q", frame)
		}
	}
	// Anchor invariant: no rendered row may exceed the resolved anchor
	// plus the logo block (logo rows are the widest allowed content).
	maxAllowed := screenWidth()
	if maxAllowed < 80 {
		maxAllowed = 80 // logo block width
	}
	for _, ln := range strings.Split(out, "\n") {
		if w := visibleLen(ln); w > maxAllowed {
			t.Errorf("welcome row exceeds anchor %d (got %d): %q", maxAllowed, w, ln)
		}
	}
}
