package i18nparity

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// writeLocale writes a tiny JSON locale into a temp dir.
func writeLocale(t *testing.T, dir, name string, keys map[string]string) {
	t.Helper()
	data := "{\n"
	first := true
	for k, v := range keys {
		if !first {
			data += ",\n"
		}
		data += "  \"" + k + "\": \"" + v + "\""
		first = false
	}
	data += "\n}\n"
	if err := os.WriteFile(filepath.Join(dir, name+".json"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadLocales(t *testing.T) {
	dir := t.TempDir()
	writeLocale(t, dir, "en", map[string]string{"a": "A", "b": "B"})
	writeLocale(t, dir, "pt-BR", map[string]string{"a": "AA", "c": "CC"})

	locales, err := LoadLocales(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(locales) != 2 {
		t.Fatalf("expected 2 locales, got %d", len(locales))
	}
	// Sorted alphabetically: en before pt-BR.
	if locales[0].Name != "en" || locales[1].Name != "pt-BR" {
		t.Errorf("locale ordering = [%s, %s], want [en, pt-BR]", locales[0].Name, locales[1].Name)
	}
}

func TestLoadLocales_Empty(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadLocales(dir)
	if err == nil {
		t.Fatal("expected error for empty dir")
	}
}

func TestMissingByLocale(t *testing.T) {
	locales := []Locale{
		{Name: "en", Keys: map[string]string{"a": "1", "b": "2", "c": "3"}},
		{Name: "pt-BR", Keys: map[string]string{"a": "1", "b": "2"}}, // missing "c"
		{Name: "es", Keys: map[string]string{"a": "1"}},              // missing "b", "c"
	}
	got := MissingByLocale(locales)

	if !reflect.DeepEqual(got["pt-BR"], []string{"c"}) {
		t.Errorf("pt-BR missing = %v, want [c]", got["pt-BR"])
	}
	wantEs := []string{"b", "c"}
	sort.Strings(got["es"])
	if !reflect.DeepEqual(got["es"], wantEs) {
		t.Errorf("es missing = %v, want %v", got["es"], wantEs)
	}
	if _, ok := got["en"]; ok {
		t.Errorf("en should have no missing keys (it's the superset)")
	}
}

func TestMissingByLocale_AllConsistent(t *testing.T) {
	locales := []Locale{
		{Name: "en", Keys: map[string]string{"a": "1"}},
		{Name: "pt-BR", Keys: map[string]string{"a": "1"}},
	}
	if got := MissingByLocale(locales); len(got) != 0 {
		t.Errorf("expected no missing keys, got %v", got)
	}
}

func TestScanUsages(t *testing.T) {
	dir := t.TempDir()
	goFile := `package x

import "github.com/diillson/chatcli/i18n"

func a() {
	_ = i18n.T("key.one")
	_ = i18n.T("key.two", "arg")
	_ = unrelated()
}
func b() {
	// dynamic key — ignored by static scan
	k := "key.three"
	_ = i18n.T(k)
}
func unrelated() string { return "" }
`
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte(goFile), 0o644); err != nil {
		t.Fatal(err)
	}
	// Test files must be ignored.
	if err := os.WriteFile(filepath.Join(dir, "x_test.go"), []byte(goFile), 0o644); err != nil {
		t.Fatal(err)
	}

	usages, err := ScanUsages(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(usages) != 2 {
		t.Fatalf("expected 2 static usages (literal keys), got %d: %+v", len(usages), usages)
	}
	gotKeys := []string{usages[0].Key, usages[1].Key}
	sort.Strings(gotKeys)
	if !reflect.DeepEqual(gotKeys, []string{"key.one", "key.two"}) {
		t.Errorf("keys = %v, want [key.one, key.two]", gotKeys)
	}
}

func TestScanUsages_ExcludesDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "vendor", "foo"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := `package foo
import "github.com/diillson/chatcli/i18n"
func _() { _ = i18n.T("excluded.key") }
`
	if err := os.WriteFile(filepath.Join(dir, "vendor", "foo", "y.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	usages, err := ScanUsages(dir, []string{"vendor"})
	if err != nil {
		t.Fatal(err)
	}
	if len(usages) != 0 {
		t.Errorf("expected vendor dir to be skipped, got: %+v", usages)
	}
}

func TestUnknownUsages(t *testing.T) {
	locales := []Locale{
		{Name: "en", Keys: map[string]string{"known.key": "x"}},
	}
	usages := []UsageRef{
		{Key: "known.key", File: "a.go", Line: 1},
		{Key: "missing.key", File: "b.go", Line: 5},
	}
	unknown := UnknownUsages(usages, locales)
	if len(unknown) != 1 {
		t.Fatalf("expected 1 unknown, got %d", len(unknown))
	}
	if unknown[0].Key != "missing.key" {
		t.Errorf("unknown key = %q, want missing.key", unknown[0].Key)
	}
}
