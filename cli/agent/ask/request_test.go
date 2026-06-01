package ask

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseRequest_Envelope(t *testing.T) {
	in := `{"questions":[{"header":"DB","question":"Which DB?","options":[{"label":"Postgres","description":"ACID"},{"label":"SQLite"}]}]}`
	qs, err := ParseRequest(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(qs) != 1 {
		t.Fatalf("want 1 question, got %d", len(qs))
	}
	if qs[0].Header != "DB" || len(qs[0].Options) != 2 {
		t.Fatalf("bad parse: %+v", qs[0])
	}
}

func TestParseRequest_BareArray(t *testing.T) {
	in := `[{"header":"H","question":"Q?","options":[{"label":"A"}]}]`
	qs, err := ParseRequest(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(qs) != 1 || qs[0].Options[0].Label != "A" {
		t.Fatalf("bad parse: %+v", qs)
	}
}

func TestParseRequest_Errors(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"empty", ``},
		{"not json", `hello`},
		{"no questions", `{"questions":[]}`},
		{"too many", `{"questions":[` + strings.Repeat(`{"header":"h","question":"q","options":[{"label":"a"}]},`, MaxQuestions) + `{"header":"h","question":"q","options":[{"label":"a"}]}]}`},
		{"missing header", `{"questions":[{"question":"q","options":[{"label":"a"}]}]}`},
		{"missing question text", `{"questions":[{"header":"h","options":[{"label":"a"}]}]}`},
		{"no options", `{"questions":[{"header":"h","question":"q","options":[]}]}`},
		{"empty option label", `{"questions":[{"header":"h","question":"q","options":[{"label":""}]}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseRequest(tt.in); err == nil {
				t.Fatalf("expected error for %q", tt.in)
			}
		})
	}
}

func TestParseRequest_TooManyOptions(t *testing.T) {
	var opts []string
	for i := 0; i < MaxOptions+1; i++ {
		opts = append(opts, `{"label":"o"}`)
	}
	in := `{"questions":[{"header":"h","question":"q","options":[` + strings.Join(opts, ",") + `]}]}`
	if _, err := ParseRequest(in); err == nil {
		t.Fatal("expected too-many-options error")
	}
}

func TestFormatResult_HasJSONBlock(t *testing.T) {
	answers := []Answer{
		{Header: "DB", Selected: []string{"Postgres"}},
		{Header: "Targets", Selected: []string{"staging", "prod"}},
		{Header: "Region", Other: "eu-west"},
	}
	out := FormatResult(answers)
	if !strings.Contains(out, "User answered:") {
		t.Error("missing prose header")
	}
	if !strings.Contains(out, "Postgres") || !strings.Contains(out, "staging, prod") || !strings.Contains(out, `Other: "eu-west"`) {
		t.Errorf("missing summary content: %s", out)
	}
	// The trailing line must be parseable JSON.
	idx := strings.Index(out, `{"answers"`)
	if idx < 0 {
		t.Fatalf("no JSON block: %s", out)
	}
	var parsed struct {
		Answers []Answer `json:"answers"`
	}
	if err := json.Unmarshal([]byte(out[idx:]), &parsed); err != nil {
		t.Fatalf("JSON block not parseable: %v", err)
	}
	if len(parsed.Answers) != 3 {
		t.Fatalf("want 3 answers, got %d", len(parsed.Answers))
	}
}

func TestDefaultAnswers_PicksFirst(t *testing.T) {
	qs := []Question{
		{Header: "DB", Options: []Option{{Label: "Postgres"}, {Label: "SQLite"}}},
		{Header: "Env", Options: []Option{{Label: "staging"}}},
	}
	ans := DefaultAnswers(qs)
	if len(ans) != 2 || ans[0].Selected[0] != "Postgres" || ans[1].Selected[0] != "staging" {
		t.Fatalf("bad defaults: %+v", ans)
	}
}

func TestFallbackResult_Mentions(t *testing.T) {
	qs := []Question{{Header: "DB", Options: []Option{{Label: "Postgres"}}}}
	out := FallbackResult(qs)
	if !strings.Contains(out, "No interactive UI") || !strings.Contains(out, "Postgres") {
		t.Errorf("bad fallback: %s", out)
	}
}

func TestCanceledResult_NotError(t *testing.T) {
	out := CanceledResult()
	if !strings.Contains(out, "canceled") || !strings.Contains(out, `"canceled":true`) {
		t.Errorf("bad canceled result: %s", out)
	}
}

func TestSchemaJSON_Valid(t *testing.T) {
	if !json.Valid([]byte(SchemaJSON())) {
		t.Fatal("SchemaJSON is not valid JSON")
	}
}
