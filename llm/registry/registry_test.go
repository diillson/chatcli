package registry

import (
	"context"
	"testing"
	"time"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
)

type mockClient struct{ name string }

func (m *mockClient) GetModelName() string { return m.name }
func (m *mockClient) SendPrompt(_ context.Context, _ string, _ []models.Message, _ int) (string, error) {
	return "ok", nil
}

func TestRegisterAndGet(t *testing.T) {
	r := NewRegistry()
	r.Register(ProviderInfo{
		Name:        "TEST",
		DisplayName: "Test Provider",
		Factory: func(cfg ProviderConfig) (client.LLMClient, error) {
			return &mockClient{name: cfg.Model}, nil
		},
	})

	info, ok := r.Get("TEST")
	if !ok {
		t.Fatal("expected to find TEST provider")
	}
	if info.DisplayName != "Test Provider" {
		t.Errorf("expected 'Test Provider', got %q", info.DisplayName)
	}
}

func TestGetCaseInsensitive(t *testing.T) {
	r := NewRegistry()
	r.Register(ProviderInfo{Name: "MyProvider"})

	_, ok := r.Get("myprovider")
	if !ok {
		t.Error("expected case-insensitive lookup to work")
	}
}

func TestList(t *testing.T) {
	r := NewRegistry()
	r.Register(ProviderInfo{Name: "BETA"})
	r.Register(ProviderInfo{Name: "ALPHA"})

	names := r.List()
	if len(names) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(names))
	}
	if names[0] != "ALPHA" || names[1] != "BETA" {
		t.Errorf("expected sorted [ALPHA, BETA], got %v", names)
	}
}

func TestCreateClient(t *testing.T) {
	r := NewRegistry()
	r.Register(ProviderInfo{
		Name:         "TEST",
		RequiresAuth: true,
		Factory: func(cfg ProviderConfig) (client.LLMClient, error) {
			return &mockClient{name: cfg.Model}, nil
		},
	})

	// Without API key should fail
	_, err := r.CreateClient("TEST", ProviderConfig{Model: "test-1"})
	if err == nil {
		t.Error("expected error without API key")
	}

	// With API key should work
	c, err := r.CreateClient("TEST", ProviderConfig{APIKey: "key", Model: "test-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.GetModelName() != "test-1" {
		t.Errorf("expected model 'test-1', got %q", c.GetModelName())
	}
}

func TestCreateClientUnknownProvider(t *testing.T) {
	r := NewRegistry()
	_, err := r.CreateClient("UNKNOWN", ProviderConfig{})
	if err == nil {
		t.Error("expected error for unknown provider")
	}
}

func TestRegisterDuplicateOverwrites(t *testing.T) {
	r := NewRegistry()
	r.Register(ProviderInfo{Name: "TEST", DisplayName: "V1"})
	r.Register(ProviderInfo{Name: "TEST", DisplayName: "V2"})

	info, _ := r.Get("TEST")
	if info.DisplayName != "V2" {
		t.Errorf("expected 'V2', got %q", info.DisplayName)
	}
}

func TestConcurrentAccess(t *testing.T) {
	r := NewRegistry()
	done := make(chan bool, 20)

	for i := 0; i < 10; i++ {
		go func(i int) {
			r.Register(ProviderInfo{Name: "P" + time.Now().String()})
			done <- true
		}(i)
		go func() {
			r.List()
			done <- true
		}()
	}

	for i := 0; i < 20; i++ {
		<-done
	}
}
