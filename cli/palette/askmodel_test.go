package palette

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/diillson/chatcli/cli/agent/ask"
)

// askSend feeds one key message through Update and returns the resulting askModel.
func askSend(t *testing.T, m askModel, msg tea.KeyMsg) askModel {
	t.Helper()
	out, _ := m.Update(msg)
	am, ok := out.(askModel)
	if !ok {
		t.Fatalf("Update did not return askModel, got %T", out)
	}
	return am
}

func askKey(tp tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: tp} }

func askRunes(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func singleQ() ask.Question {
	return ask.Question{
		Header: "DB", Question: "Which DB?",
		Options: []ask.Option{{Label: "Postgres"}, {Label: "SQLite"}},
	}
}

func multiQ() ask.Question {
	return ask.Question{
		Header: "Env", Question: "Which envs?", MultiSelect: true,
		Options: []ask.Option{{Label: "staging"}, {Label: "prod"}, {Label: "dev"}},
	}
}

func TestAsk_SingleSelect_PicksAndFinishes(t *testing.T) {
	m := NewAsk([]ask.Question{singleQ()})
	m = askSend(t, m, askKey(tea.KeyDown))  // cursor -> SQLite
	m = askSend(t, m, askKey(tea.KeyEnter)) // pick + finish (only one question)
	if m.Canceled() {
		t.Fatal("should not be canceled")
	}
	ans := m.Answers()
	if len(ans) != 1 || len(ans[0].Selected) != 1 || ans[0].Selected[0] != "SQLite" {
		t.Fatalf("bad answer: %+v", ans)
	}
}

func TestAsk_SingleSelect_SpaceMarksWithoutAdvancing(t *testing.T) {
	m := NewAsk([]ask.Question{singleQ(), multiQ()})
	m = askSend(t, m, askKey(tea.KeyDown))  // cursor -> SQLite (index 1)
	m = askSend(t, m, askKey(tea.KeySpace)) // mark the radio, do NOT advance
	if m.qIndex != 0 {
		t.Fatalf("space must not advance; qIndex=%d", m.qIndex)
	}
	if m.single[0] != 1 {
		t.Fatalf("space must set the radio to cursor; single[0]=%d", m.single[0])
	}
	m = askSend(t, m, askKey(tea.KeyEnter)) // now confirm + advance to q2
	if m.qIndex != 1 {
		t.Fatalf("enter should advance to q2; qIndex=%d", m.qIndex)
	}
}

func TestAsk_MultiSelect_TogglesThenConfirms(t *testing.T) {
	m := NewAsk([]ask.Question{multiQ()})
	m = askSend(t, m, askKey(tea.KeySpace)) // toggle staging (cursor 0)
	m = askSend(t, m, askKey(tea.KeyDown))
	m = askSend(t, m, askKey(tea.KeyDown))  // cursor -> dev (index 2)
	m = askSend(t, m, askKey(tea.KeySpace)) // toggle dev
	m = askSend(t, m, askKey(tea.KeyEnter)) // confirm + finish
	ans := m.Answers()
	if len(ans) != 1 {
		t.Fatalf("want 1 answer, got %d", len(ans))
	}
	if len(ans[0].Selected) != 2 || ans[0].Selected[0] != "staging" || ans[0].Selected[1] != "dev" {
		t.Fatalf("bad multi answer (want staging,dev sorted): %+v", ans[0].Selected)
	}
}

// stripANSI removes color escapes so the rendered overlay is readable in test
// output and the [x]/[ ] markers can be asserted on plain text.
func stripANSITest(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b { // ESC — skip until the terminating letter of the CSI
			for i < len(s) && !((s[i] >= 'a' && s[i] <= 'z') || (s[i] >= 'A' && s[i] <= 'Z')) {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func TestAsk_MultiSelect_RendersCheckmark(t *testing.T) {
	m := NewAsk([]ask.Question{multiQ()}) // staging, prod, dev — multiSelect

	before := stripANSITest(m.View())
	if !strings.Contains(before, "[ ] staging") {
		t.Fatalf("expected unchecked staging before toggle.\n%s", before)
	}

	m = askSend(t, m, askKey(tea.KeySpace)) // toggle staging (cursor 0)
	m = askSend(t, m, askKey(tea.KeyDown))
	m = askSend(t, m, askKey(tea.KeyDown))
	m = askSend(t, m, askKey(tea.KeySpace)) // toggle dev (cursor 2)

	after := stripANSITest(m.View())
	t.Logf("rendered multi-select overlay after toggling staging + dev:\n%s", after)

	if !strings.Contains(after, "[x] staging") {
		t.Errorf("staging should be checked: \n%s", after)
	}
	if !strings.Contains(after, "[x] dev") {
		t.Errorf("dev should be checked: \n%s", after)
	}
	if !strings.Contains(after, "[ ] prod") {
		t.Errorf("prod should remain unchecked: \n%s", after)
	}

	// Toggling staging again must clear it.
	m = askSend(t, m, askKey(tea.KeyUp))
	m = askSend(t, m, askKey(tea.KeyUp)) // back to staging
	m = askSend(t, m, askKey(tea.KeySpace))
	cleared := stripANSITest(m.View())
	if !strings.Contains(cleared, "[ ] staging") {
		t.Errorf("staging should be unchecked after second toggle: \n%s", cleared)
	}
}

func TestAsk_Other_SingleSelect(t *testing.T) {
	m := NewAsk([]ask.Question{singleQ()})
	// move to the "Other" row (index 2 = len(options))
	m = askSend(t, m, askKey(tea.KeyDown))
	m = askSend(t, m, askKey(tea.KeyDown))
	if m.cursor != m.otherRow() {
		t.Fatalf("cursor not on Other row: %d (want %d)", m.cursor, m.otherRow())
	}
	m = askSend(t, m, askKey(tea.KeyEnter)) // enter editing
	if !m.editing {
		t.Fatal("should be editing")
	}
	m = askSend(t, m, askRunes("mongo"))
	m = askSend(t, m, askKey(tea.KeyEnter)) // commit + finish
	ans := m.Answers()
	if len(ans) != 1 || ans[0].Other != "mongo" || len(ans[0].Selected) != 0 {
		t.Fatalf("bad other answer: %+v", ans)
	}
}

func TestAsk_EscCancels(t *testing.T) {
	m := NewAsk([]ask.Question{singleQ()})
	m = askSend(t, m, askKey(tea.KeyEsc))
	if !m.Canceled() {
		t.Fatal("esc on first question should cancel")
	}
}

func TestAsk_EscGoesBackOnLaterQuestion(t *testing.T) {
	m := NewAsk([]ask.Question{singleQ(), multiQ()})
	m = askSend(t, m, askKey(tea.KeyEnter)) // pick Postgres, advance to q2
	if m.qIndex != 1 {
		t.Fatalf("should be on q2, got qIndex=%d", m.qIndex)
	}
	m = askSend(t, m, askKey(tea.KeyEsc)) // back to q1
	if m.qIndex != 0 {
		t.Fatalf("esc should go back to q1, got %d", m.qIndex)
	}
	if m.Canceled() {
		t.Fatal("should not be canceled")
	}
}

func TestAsk_TwoQuestions_Flow(t *testing.T) {
	m := NewAsk([]ask.Question{singleQ(), multiQ()})
	m = askSend(t, m, askKey(tea.KeyEnter)) // q1: Postgres, advance
	m = askSend(t, m, askKey(tea.KeySpace)) // q2: toggle staging
	m = askSend(t, m, askKey(tea.KeyEnter)) // q2: confirm + finish
	ans := m.Answers()
	if len(ans) != 2 {
		t.Fatalf("want 2 answers, got %d", len(ans))
	}
	if ans[0].Selected[0] != "Postgres" {
		t.Errorf("q1 wrong: %+v", ans[0])
	}
	if len(ans[1].Selected) != 1 || ans[1].Selected[0] != "staging" {
		t.Errorf("q2 wrong: %+v", ans[1])
	}
}

func TestAsk_EditingEscKeepsOpen(t *testing.T) {
	m := NewAsk([]ask.Question{singleQ()})
	m = askSend(t, m, askKey(tea.KeyDown))
	m = askSend(t, m, askKey(tea.KeyDown)) // Other row
	m = askSend(t, m, askKey(tea.KeyEnter))
	m = askSend(t, m, askRunes("x"))
	m = askSend(t, m, askKey(tea.KeyEsc)) // cancel edit, stay open
	if m.editing {
		t.Fatal("esc should exit editing")
	}
	if m.Canceled() || m.quitting {
		t.Fatal("esc during editing must not cancel the whole overlay")
	}
}

func TestAsk_ViewRendersWithoutPanic(t *testing.T) {
	m := NewAsk([]ask.Question{singleQ(), multiQ()})
	if m.View() == "" {
		t.Fatal("view should render the first question")
	}
	m.editing = true
	if m.View() == "" {
		t.Fatal("view should render while editing")
	}
}
