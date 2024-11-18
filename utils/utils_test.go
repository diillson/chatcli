package utils

import (
	"os"
	"testing"
)

func TestGetEnvOrDefault(t *testing.T) {
	os.Setenv("TEST_ENV", "value")
	value := GetEnvOrDefault("TEST_ENV", "default")
	if value != "value" {
		t.Errorf("Esperado 'value', obtido '%s'", value)
	}

	value = GetEnvOrDefault("NON_EXISTENT", "default")
	if value != "default" {
		t.Errorf("Esperado 'default', obtido '%s'", value)
	}
}

func TestGenerateUUID(t *testing.T) {
	uuid := GenerateUUID()
	if uuid == "" {
		t.Error("UUID vazio")
	}
}
