package utils

import (
	"testing"

	"go.uber.org/zap"
)

func TestNewHTTPClient(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	client := NewHTTPClient(logger, 30)
	if client == nil {
		t.Error("Cliente HTTP é nil")
	}
}
