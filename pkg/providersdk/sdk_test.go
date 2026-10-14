/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package providersdk

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/diillson/chatcli/pkg/providerproto"
)

type fakeFull struct {
	gate chan struct{} // when set, Chat waits on it
}

func (fakeFull) Info() providerproto.ProviderInfo {
	return providerproto.ProviderInfo{Name: "FAKE", DisplayName: map[string]string{"en": "Fake"}, Version: "0"}
}
func (fakeFull) Mode() providerproto.Mode { return providerproto.ModeFull }
func (fakeFull) ChatCapabilities() providerproto.ChatCapabilities {
	return providerproto.ChatCapabilities{Stream: true}
}
func (f fakeFull) Chat(ctx context.Context, p providerproto.ChatParams, emit func(providerproto.ChatDelta) error) (providerproto.ChatResult, error) {
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return providerproto.ChatResult{}, ctx.Err()
		}
	}
	if emit != nil {
		for _, w := range []string{"a", "b", "c"} {
			if err := emit(providerproto.ChatDelta{Text: w}); err != nil {
				return providerproto.ChatResult{}, err
			}
		}
	}
	return providerproto.ChatResult{
		Message:    providerproto.Message{Role: providerproto.RoleAssistant, Content: []providerproto.Part{{Type: providerproto.PartText, Text: "abc"}}},
		StopReason: providerproto.StopEnd,
	}, nil
}

// host starts p behind ServeConn and returns a host-side Conn plus a
// channel of the chat/delta notifications it received.
func host(t *testing.T, p Provider) (*providerproto.Conn, <-chan providerproto.ChatDelta, <-chan error) {
	t.Helper()
	pr, hw := io.Pipe()
	hr, pw := io.Pipe()
	served := make(chan error, 1)
	go func() {
		served <- ServeConn(context.Background(), p, pr, pw)
		_ = pw.Close()
	}()
	deltas := make(chan providerproto.ChatDelta, 64)
	h := func(_ context.Context, req *providerproto.Request) (any, error) {
		if req.Method == providerproto.MethodChatDelta {
			var d providerproto.ChatDelta
			if err := decode(req.Params, &d); err == nil {
				deltas <- d
			}
		}
		return nil, nil
	}
	c := providerproto.NewConn(hr, hw, h, providerproto.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = c.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = hw.Close()
	})
	return c, deltas, served
}

func initialize(t *testing.T, c *providerproto.Conn) providerproto.InitializeResult {
	t.Helper()
	var res providerproto.InitializeResult
	err := c.Call(context.Background(), providerproto.MethodInitialize, providerproto.InitializeParams{
		ProtocolVersions: []string{providerproto.ProtocolVersion},
		Host:             providerproto.HostInfo{Name: "test", Version: "0"},
	}, &res)
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	return res
}

func code(err error) int {
	var perr *providerproto.Error
	if errors.As(err, &perr) {
		return perr.Code
	}
	return 0
}

func TestInitializeAnnouncesDerivedCapabilities(t *testing.T) {
	c, _, _ := host(t, fakeFull{})
	if err := c.Call(context.Background(), providerproto.MethodModelsList, nil, nil); code(err) != providerproto.ErrCodeInvalidRequest {
		t.Fatalf("call before initialize = %v, want invalid request", err)
	}
	res := initialize(t, c)
	if res.Mode != providerproto.ModeFull || res.Provider.Name != "FAKE" {
		t.Fatalf("result = %+v", res)
	}
	if !res.Capabilities.Chat.Stream || res.Capabilities.Models || res.Capabilities.Auth.Login {
		t.Fatalf("capabilities = %+v, want only chat streaming", res.Capabilities)
	}
	if err := c.Call(context.Background(), providerproto.MethodModelsList, nil, nil); code(err) != providerproto.ErrCodeMethodNotFound {
		t.Fatalf("unimplemented method = %v, want method not found", err)
	}
}

func TestInitializeRejectsUnknownProtocolVersion(t *testing.T) {
	c, _, _ := host(t, fakeFull{})
	err := c.Call(context.Background(), providerproto.MethodInitialize, providerproto.InitializeParams{
		ProtocolVersions: []string{"99"},
	}, nil)
	if code(err) != providerproto.ErrCodeInvalidRequest {
		t.Fatalf("err = %v", err)
	}
}

func TestStreamingDeltasArriveBeforeResult(t *testing.T) {
	c, deltas, _ := host(t, fakeFull{})
	initialize(t, c)
	var res providerproto.ChatResult
	if err := c.Call(context.Background(), providerproto.MethodChatSend, providerproto.ChatParams{Model: "m", Stream: true}, &res); err != nil {
		t.Fatal(err)
	}
	var got []string
	for len(deltas) > 0 {
		d := <-deltas
		if d.RequestID == nil {
			t.Error("delta without requestId")
		}
		got = append(got, d.Text)
	}
	if strings.Join(got, "") != "abc" {
		t.Fatalf("deltas observed before the result = %v, want a b c", got)
	}
}

// Requests that arrived before shutdown are still served; later ones are
// refused — decided by arrival order, not by when a handler starts.
func TestShutdownServesEarlierRequestsOnly(t *testing.T) {
	gate := make(chan struct{})
	c, _, served := host(t, fakeFull{gate: gate})
	initialize(t, c)

	var wg sync.WaitGroup
	var chatErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		chatErr = c.Call(context.Background(), providerproto.MethodChatSend, providerproto.ChatParams{Model: "m"}, nil)
	}()
	time.Sleep(50 * time.Millisecond) // chat/send is on the wire first
	if err := c.Call(context.Background(), providerproto.MethodShutdown, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Call(context.Background(), providerproto.MethodChatSend, providerproto.ChatParams{Model: "m"}, nil); code(err) != providerproto.ErrCodeInvalidRequest {
		t.Fatalf("request after shutdown = %v, want refused", err)
	}
	close(gate)
	wg.Wait()
	if chatErr != nil {
		t.Fatalf("request sent before shutdown was refused: %v", chatErr)
	}

	_ = c.Notify(context.Background(), providerproto.MethodExit, nil)
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("ServeConn after exit = %v, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ServeConn did not return after exit")
	}
}

type badDialect struct{}

func (badDialect) Info() providerproto.ProviderInfo {
	return providerproto.ProviderInfo{Name: "BAD", DisplayName: map[string]string{"en": "Bad"}}
}
func (badDialect) Mode() providerproto.Mode { return providerproto.ModeDialect }

func TestValidate(t *testing.T) {
	if err := Validate(fakeFull{}); err != nil {
		t.Fatalf("valid provider rejected: %v", err)
	}
	if err := Validate(badDialect{}); err == nil {
		t.Fatal("dialect mode without DialectProvider accepted")
	}
	if err := ServeConn(context.Background(), badDialect{}, strings.NewReader(""), io.Discard); err == nil {
		t.Fatal("ServeConn must refuse an invalid provider")
	}
}
