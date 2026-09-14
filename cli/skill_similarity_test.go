package cli

import (
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

// A candidate about one team folds into the installed skill about the
// competence; an unrelated candidate stays new; a candidate that already
// names the skill is left to the evolve path.
func TestRedirectToExistingFoldsSiblings(t *testing.T) {
	existing := []skillMeta{
		{Name: "team-match-info", Description: "Next match, schedule and standings of a football team using static sources that work with @webfetch", Triggers: []string{"próximo jogo", "agenda", "brasileirão", "libertadores"}},
		{Name: "k8s-deploy", Description: "Deploy a service to Kubernetes with Helm", Triggers: []string{"deploy", "helm", "kubernetes"}},
	}
	cand := skillCandidate{Name: "corinthians-match-info", Description: "Consultar próximo jogo, agenda e classificação do Corinthians no Brasileirão com fontes estáticas via @webfetch", Triggers: []string{"próximo jogo corinthians", "agenda corinthians", "brasileirão"}, Body: "# steps"}
	name, score := closestSkill(existing, cand)
	if name != "team-match-info" || score < skillSimilarityThreshold {
		t.Fatalf("closest = %q score %.2f", name, score)
	}
	got, ok := redirectToExisting(existing, cand)
	if !ok || got.Name != "team-match-info" || got.Improvement != "# steps" || got.Body != "" {
		t.Fatalf("redirect = %+v ok=%v", got, ok)
	}

	other := skillCandidate{Name: "pdf-table-extract", Description: "Extract tables from PDF reports into CSV", Triggers: []string{"pdf", "tabela"}, Body: "# x"}
	if _, ok := redirectToExisting(existing, other); ok {
		t.Fatal("an unrelated competence stays new")
	}
	same := skillCandidate{Name: "k8s-deploy", Description: "Deploy a service to Kubernetes with Helm", Body: "# y"}
	if _, ok := redirectToExisting(existing, same); ok {
		t.Fatal("a candidate that already names the skill is the evolve path's business")
	}
	if _, ok := redirectToExisting(existing, skillCandidate{Name: "x", Improvement: "only"}); ok {
		t.Fatal("an evolve candidate is never redirected")
	}
	if n, s := closestSkill(nil, cand); n != "" || s != 0 {
		t.Fatal("no skills, no match")
	}
	if len(skillKeywords("the and para skill info Deploy Kubernetes")) != 2 {
		t.Fatalf("keywords = %v", skillKeywords("the and para skill info Deploy Kubernetes"))
	}
	if overlap(nil, nil) != 0 || overlap(skillKeywords("alpha beta"), skillKeywords("alpha beta")) != 0 {
		t.Fatal("empty sets and fewer than three shared words never match")
	}
}

// A skill that declares tools is measured: used when one of them ran,
// compared without case or the @ prefix. One without declared tools is
// activated, never judged.
func TestSkillUseIsMeasuredOnlyWhenDeclared(t *testing.T) {
	executed := map[string]bool{"webfetch": true, "coder": true}
	if u := skillUseFor([]string{"@WebFetch", "@websearch"}, executed); !u.Measurable || !u.Used {
		t.Fatalf("declared and executed: %+v", u)
	}
	if u := skillUseFor([]string{"@maps"}, executed); !u.Measurable || u.Used {
		t.Fatalf("declared and idle: %+v", u)
	}
	if u := skillUseFor(nil, executed); u.Measurable || u.Used {
		t.Fatalf("nothing declared: %+v", u)
	}
	a := &AgentMode{logger: zap.NewNop()}
	a.noteRunToolName("@WebFetch")
	a.noteRunToolName("  ")
	if !a.runToolNames["webfetch"] || len(a.runToolNames) != 1 {
		t.Fatalf("tool names = %v", a.runToolNames)
	}
	a.resetPerRunState()
	if a.runToolNames != nil {
		t.Fatal("a new run forgets the tools")
	}

	l := loadSkillStatsAt("")
	now := time.Now()
	l.recordRun([]string{"maps", "free"}, 1, 1, 0.1, true, now, map[string]skillUse{"maps": {Measurable: true, Used: false}})
	l.recordRun([]string{"maps"}, 1, 1, 0.1, true, now, map[string]skillUse{"maps": {Measurable: true, Used: true}})
	if m := l.Skills["maps"]; m.Measured != 2 || m.Used != 1 {
		t.Fatalf("maps = %+v", m)
	}
	if f := l.Skills["free"]; f.Measured != 0 || f.Used != 0 {
		t.Fatalf("free = %+v", f)
	}
	lines := strings.Join(renderSkillStats(l, nil, ""), "\n")
	if !strings.Contains(lines, "1") || !strings.Contains(strings.ToLower(lines), "tool") {
		t.Fatalf("view must show the use ratio and the unmeasured case:\n%s", lines)
	}
	if (&AgentMode{}).skillUsesThisRun() != nil {
		t.Fatal("no persona handler, nothing measured")
	}
}
