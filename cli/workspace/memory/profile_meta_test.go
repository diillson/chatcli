package memory

import (
	"strings"
	"testing"
	"time"
)

// All fixtures here are SYNTHETIC — never put real user data in tests.

func TestFieldMeta_ProvenanceAndReaffirmation(t *testing.T) {
	ps := NewUserProfileStore(t.TempDir(), testLogger())

	// Extraction writes an inferred value.
	ps.UpdateWithSource(map[string]string{"role": "Backend Dev"}, FieldSourceExtraction)
	m := ps.Get().FieldMeta["role"]
	if m.Source != FieldSourceExtraction || m.UpdatedAt.IsZero() || m.ConfirmedAt.IsZero() {
		t.Fatalf("extraction meta wrong: %+v", m)
	}

	// The user restating the SAME value changes nothing but re-affirms it and
	// upgrades trust to user-stated.
	before := m.ConfirmedAt
	time.Sleep(5 * time.Millisecond)
	if changed := ps.Update(map[string]string{"role": "Backend Dev"}); changed {
		t.Fatal("identical value must not report change")
	}
	m = ps.Get().FieldMeta["role"]
	if !m.ConfirmedAt.After(before) {
		t.Error("re-affirmation must bump ConfirmedAt")
	}
	if m.Source != FieldSourceUser {
		t.Errorf("user re-affirmation must upgrade source, got %q", m.Source)
	}

	// Re-affirmation must survive restarts (persisted even without change).
	ps2 := NewUserProfileStore(ps2Dir(t, ps), testLogger())
	if got := ps2.Get().FieldMeta["role"].Source; got != FieldSourceUser {
		t.Errorf("meta must persist, got source %q", got)
	}
}

// ps2Dir extracts the store directory for reload tests.
func ps2Dir(t *testing.T, ps *UserProfileStore) string {
	t.Helper()
	return strings.TrimSuffix(ps.path, "/user_profile.json")
}

func TestFormatForPrompt_FlagsStaleFields(t *testing.T) {
	ps := NewUserProfileStore(t.TempDir(), testLogger())
	ps.Update(map[string]string{"company": "Empresa Fictícia"})

	// Age the confirmation artificially beyond the staleness window.
	ps.mu.Lock()
	m := ps.profile.FieldMeta["company"]
	m.ConfirmedAt = time.Now().Add(-staleAfter - 24*time.Hour)
	m.UpdatedAt = m.ConfirmedAt
	ps.profile.FieldMeta["company"] = m
	ps.mu.Unlock()

	prompt := ps.FormatForPrompt()
	if !strings.Contains(prompt, "Possibly stale") || !strings.Contains(prompt, "company") {
		t.Errorf("stale field must be flagged:\n%s", prompt)
	}

	// Fresh re-confirmation clears the flag.
	ps.Update(map[string]string{"company": "Empresa Fictícia"})
	if strings.Contains(ps.FormatForPrompt(), "Possibly stale") {
		t.Error("re-confirmed field must not be flagged stale")
	}
}

func TestSensitive_AutoFlagAndGuardrail(t *testing.T) {
	ps := NewUserProfileStore(t.TempDir(), testLogger())

	ps.Update(map[string]string{"renda_mensal": "um valor qualquer", "shell": "shell-y"})
	p := ps.Get()
	if !p.FieldMeta["pref:renda_mensal"].Sensitive {
		t.Fatal("finance-keyed preference must be auto-flagged sensitive")
	}
	if p.FieldMeta["pref:shell"].Sensitive {
		t.Fatal("neutral preference must not be flagged")
	}

	prompt := ps.FormatForPrompt()
	if !strings.Contains(prompt, "renda_mensal [sensitive]") {
		t.Errorf("sensitive preference must carry the tag:\n%s", prompt)
	}
	if !strings.Contains(prompt, "NEVER quote them into code") {
		t.Errorf("privacy guardrail line missing:\n%s", prompt)
	}

	// Explicit unmark wins over auto-detection…
	if changed := ps.Update(map[string]string{"sensitive_unmark": "renda_mensal"}); !changed {
		t.Fatal("unmark must report change")
	}
	// …but only until the next write of that key re-triggers detection; the
	// unmark itself must take effect immediately.
	if ps.Get().FieldMeta["pref:renda_mensal"].Sensitive {
		t.Error("unmark must clear the flag")
	}

	// Explicit mark on a non-sensitive-looking field.
	ps.Update(map[string]string{"sensitive_mark": "shell"})
	if !ps.Get().FieldMeta["pref:shell"].Sensitive {
		t.Error("explicit mark must set the flag")
	}
}

func TestSensitive_BackfillOnLoad(t *testing.T) {
	dir := t.TempDir()
	ps := NewUserProfileStore(dir, testLogger())
	// Simulate a pre-tracking profile: preference present, no meta.
	ps.mu.Lock()
	ps.profile.Preferences["saldo_conta"] = "um valor qualquer"
	ps.profile.FieldMeta = nil
	ps.persist()
	ps.mu.Unlock()

	ps2 := NewUserProfileStore(dir, testLogger())
	if !ps2.Get().FieldMeta["pref:saldo_conta"].Sensitive {
		t.Error("legacy sensitive preference must be back-filled on load")
	}
}

func TestStances_ParseAndSupersede(t *testing.T) {
	ps := NewUserProfileStore(t.TempDir(), testLogger())

	ps.Update(map[string]string{"stance": "preferir backends keyless :: menos atrito de setup; evitar dependência pesada :: build fica lento"})
	p := ps.Get()
	if len(p.Stances) != 2 {
		t.Fatalf("expected 2 stances, got %#v", p.Stances)
	}
	if p.Stances[0].Reason == "" {
		t.Error("stance must carry its reason")
	}

	// Restating a position supersedes its reason instead of duplicating.
	ps.Update(map[string]string{"stance": "preferir backends keyless :: zero fricção para novos usuários"})
	p = ps.Get()
	if len(p.Stances) != 2 {
		t.Fatalf("restated stance must supersede, got %#v", p.Stances)
	}
	if !strings.Contains(p.Stances[0].Reason, "fricção") {
		t.Errorf("newest reason must win: %+v", p.Stances[0])
	}

	if !strings.Contains(ps.FormatForPrompt(), "why:") {
		t.Error("FormatForPrompt must render stance reasons")
	}
}

func TestDirectives_HardVsPreferencePartition(t *testing.T) {
	ps := NewUserProfileStore(t.TempDir(), testLogger())
	ps.Update(map[string]string{"directives": "nunca usar atalho Z; evitar jargão"})

	prompt := ps.FormatForPrompt()
	if !strings.Contains(prompt, "hard rules (MUST follow): nunca usar atalho Z") {
		t.Errorf("veto must land in hard rules:\n%s", prompt)
	}
	if !strings.Contains(prompt, "preferences: evitar jargão") {
		t.Errorf("soft directive must land in preferences:\n%s", prompt)
	}
}

func TestEnvironment_SetAndMigrateLegacyPrefs(t *testing.T) {
	dir := t.TempDir()
	ps := NewUserProfileStore(dir, testLogger())

	ps.Update(map[string]string{"env_shell": "shell-y", "machine_ram": "16GB"})
	p := ps.Get()
	if p.Environment["shell"] != "shell-y" {
		t.Fatalf("env_ key must land in Environment: %#v", p.Environment)
	}
	// machine_* went in as a preference; the load-time migration moves it.
	ps2 := NewUserProfileStore(dir, testLogger())
	p2 := ps2.Get()
	if p2.Environment["machine_ram"] != "16GB" {
		t.Errorf("legacy env-ish preference must migrate: %#v / %#v", p2.Environment, p2.Preferences)
	}
	if _, still := p2.Preferences["machine_ram"]; still {
		t.Error("migrated preference must leave the preference bag")
	}
	if !strings.Contains(ps2.FormatForPrompt(), "Environment: ") {
		t.Error("FormatForPrompt must render environment")
	}
}
