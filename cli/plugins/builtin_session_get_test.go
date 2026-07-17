/*
 * ChatCLI - @session get dispatch tests.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 */
package plugins

import (
	"context"
	"strings"
	"testing"
)

// readableSessionAdapter implements SessionAdapter + SessionReader.
type readableSessionAdapter struct {
	name, query   string
	offset, limit int
}

func (r *readableSessionAdapter) Search(context.Context, string, int) (string, error) {
	return "", nil
}
func (r *readableSessionAdapter) List(context.Context) (string, error) { return "", nil }
func (r *readableSessionAdapter) Get(_ context.Context, name string, offset, limit int, query string) (string, error) {
	r.name, r.offset, r.limit, r.query = name, offset, limit, query
	return "get-ok", nil
}

// searchOnlyAdapter has no SessionReader capability.
type searchOnlyAdapter struct{}

func (searchOnlyAdapter) Search(context.Context, string, int) (string, error) { return "", nil }
func (searchOnlyAdapter) List(context.Context) (string, error)                { return "", nil }

func TestBuiltinSession_GetDispatch(t *testing.T) {
	fake := &readableSessionAdapter{}
	SetSessionAdapter(fake)
	defer SetSessionAdapter(nil)

	p := NewBuiltinSessionPlugin()
	out, err := p.Execute(context.Background(),
		[]string{`{"cmd":"get","args":{"name":"auth-design","offset":"10","limit":5,"query":"oauth"}}`})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if out != "get-ok" {
		t.Errorf("unexpected output %q", out)
	}
	if fake.name != "auth-design" || fake.offset != 10 || fake.limit != 5 || fake.query != "oauth" {
		t.Errorf("args not threaded (string offset must parse leniently): %+v", fake)
	}

	// Aliases fold into get.
	for _, alias := range []string{"read", "show", "open"} {
		if _, err := p.Execute(context.Background(), []string{`{"cmd":"` + alias + `","args":{"name":"x"}}`}); err != nil {
			t.Errorf("alias %q must dispatch to get: %v", alias, err)
		}
	}

	// Missing name is an actionable error.
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"get","args":{}}`}); err == nil || !strings.Contains(err.Error(), "name") {
		t.Errorf("missing name must error actionably, got %v", err)
	}
}

func TestBuiltinSession_GetWithoutCapability(t *testing.T) {
	SetSessionAdapter(searchOnlyAdapter{})
	defer SetSessionAdapter(nil)

	p := NewBuiltinSessionPlugin()
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"get","args":{"name":"x"}}`}); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Errorf("adapter without SessionReader must degrade gracefully, got %v", err)
	}
}
