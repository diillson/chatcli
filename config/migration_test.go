package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

func testLogger() *zap.Logger {
	l, _ := zap.NewDevelopment()
	return l
}

func TestMigrationRegistry_NeedsMigration_NoFile(t *testing.T) {
	dir := t.TempDir()
	r := NewMigrationRegistry(dir, testLogger())

	needs, current, target, err := r.NeedsMigration()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !needs {
		t.Error("expected migration needed from v0")
	}
	if current != 0 {
		t.Errorf("expected current=0, got %d", current)
	}
	if target != CurrentConfigVersion {
		t.Errorf("expected target=%d, got %d", CurrentConfigVersion, target)
	}
}

func TestMigrationRegistry_AlreadyCurrent(t *testing.T) {
	dir := t.TempDir()
	r := NewMigrationRegistry(dir, testLogger())
	_ = r.SetVersion(CurrentConfigVersion)

	needs, _, _, err := r.NeedsMigration()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if needs {
		t.Error("expected no migration needed")
	}
}

func TestMigrationRegistry_Migrate_V0toV1(t *testing.T) {
	dir := t.TempDir()
	r := NewMigrationRegistry(dir, testLogger())

	values := map[string]interface{}{
		"LLM_PROVIDER":    "openai",
		"CHATCLI_API_KEY": "sk-test",
	}

	result, err := r.Migrate(values)
	if err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	// Provider should be uppercase
	if result["LLM_PROVIDER"] != "OPENAI" {
		t.Errorf("expected 'OPENAI', got %v", result["LLM_PROVIDER"])
	}

	// Old key should be migrated
	if _, exists := result["CHATCLI_API_KEY"]; exists {
		t.Error("expected CHATCLI_API_KEY to be removed")
	}
	if result["OPENAI_API_KEY"] != "sk-test" {
		t.Errorf("expected migrated API key, got %v", result["OPENAI_API_KEY"])
	}

	// Defaults should be set
	if result["CHATCLI_SKILLS_ENABLED"] != "true" {
		t.Error("expected skills enabled by default")
	}

	// Version should be updated
	cv, _ := r.GetCurrentVersion()
	if cv.Version != CurrentConfigVersion {
		t.Errorf("expected version %d, got %d", CurrentConfigVersion, cv.Version)
	}
}

func TestMigrationRegistry_MultiStep(t *testing.T) {
	// CurrentConfigVersion is 1, so only v0→v1 runs.
	// Test that multi-step would work by checking v0→v1 is applied.
	dir := t.TempDir()
	r := NewMigrationRegistry(dir, testLogger())

	// Override the built-in v0→v1 migration with a custom one
	applied := map[string]bool{}
	r.Register(0, func(v map[string]interface{}) (map[string]interface{}, error) {
		applied["step1"] = true
		v["step1"] = true
		return v, nil
	})

	values := map[string]interface{}{}
	result, err := r.Migrate(values)
	if err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	if result["step1"] != true {
		t.Error("step1 not applied")
	}
	if !applied["step1"] {
		t.Error("custom migration step1 was not executed")
	}
}

func TestMigrationRegistry_FailedMigration(t *testing.T) {
	dir := t.TempDir()
	r := NewMigrationRegistry(dir, testLogger())

	// Override v0->v1 with a failing migration
	r.Register(0, func(v map[string]interface{}) (map[string]interface{}, error) {
		return nil, fmt.Errorf("intentional failure")
	})

	original := map[string]interface{}{"key": "value"}
	result, err := r.Migrate(original)
	if err == nil {
		t.Fatal("expected error from failing migration")
	}

	// Should return original values
	if result["key"] != "value" {
		t.Error("expected original values to be preserved")
	}
}

func TestMigrationRegistry_Backup(t *testing.T) {
	dir := t.TempDir()
	r := NewMigrationRegistry(dir, testLogger())

	values := map[string]interface{}{"test": "data"}
	path, err := r.Backup(values)
	if err != nil {
		t.Fatalf("backup failed: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Errorf("backup file not found: %v", err)
	}

	// Check backup dir exists
	backupDir := filepath.Join(dir, "backups")
	entries, _ := os.ReadDir(backupDir)
	if len(entries) == 0 {
		t.Error("expected backup files in backups dir")
	}
}

func TestMigrationRegistry_SetGetVersion(t *testing.T) {
	dir := t.TempDir()
	r := NewMigrationRegistry(dir, testLogger())

	if err := r.SetVersion(5); err != nil {
		t.Fatalf("set version failed: %v", err)
	}

	cv, err := r.GetCurrentVersion()
	if err != nil {
		t.Fatalf("get version failed: %v", err)
	}
	if cv.Version != 5 {
		t.Errorf("expected version 5, got %d", cv.Version)
	}
}
