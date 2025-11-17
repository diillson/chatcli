package utils

import (
	"fmt"
	"os"
)

func CreateTempFile(pattern, content string) (string, error) {
	tempFile, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	defer tempFile.Close()

	if _, err := tempFile.WriteString(content); err != nil {
		return "", fmt.Errorf("failed to write content: %w", err)
	}

	return tempFile.Name(), nil
}

func RemoveFile(path string) error {
	return os.Remove(path)
}
