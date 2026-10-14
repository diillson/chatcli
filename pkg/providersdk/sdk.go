/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Package providersdk builds chatcli provider plugins in Go.
//
// A plugin is a program that serves the provider protocol
// (pkg/providerproto) over stdin/stdout. Implement Provider plus the
// optional interfaces matching what the backend offers, then call Serve
// from main:
//
//	func main() {
//		if err := providersdk.Serve(context.Background(), &myProvider{}); err != nil {
//			fmt.Fprintln(os.Stderr, err)
//			os.Exit(1)
//		}
//	}
//
// Capabilities announced to the host are derived from the interfaces the
// value implements, so a plugin can never advertise a method it does not
// serve. Plugins in other languages implement the same wire protocol
// directly; see pkg/providerproto/SPEC.md.
package providersdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sync"

	"github.com/diillson/chatcli/pkg/providerproto"
)

// Provider is the mandatory part of every plugin.
type Provider interface {
	Info() providerproto.ProviderInfo
	Mode() providerproto.Mode
}

// DialectProvider is required in ModeDialect.
type DialectProvider interface {
	Dialect() providerproto.Dialect
}

// Authenticator serves auth/status, auth/login, auth/continue and
// auth/token. The plugin keeps no secrets: everything it needs later goes
// into the Credential of the final StepDone, which the host stores and
// passes back.
type Authenticator interface {
	AuthStatus(ctx context.Context, cred providerproto.Credential) (providerproto.AuthStatusResult, error)
	Login(ctx context.Context, p providerproto.AuthLoginParams) (providerproto.AuthStep, error)
	Continue(ctx context.Context, p providerproto.AuthContinueParams) (providerproto.AuthStep, error)
	Token(ctx context.Context, cred providerproto.Credential) (providerproto.AuthTokenResult, error)
}

// Logouter serves auth/logout (revoking the credential upstream).
type Logouter interface {
	Logout(ctx context.Context, cred providerproto.Credential) error
}

// ModelLister serves models/list.
type ModelLister interface {
	ListModels(ctx context.Context, cred providerproto.Credential) ([]providerproto.Model, error)
}

// Chatter serves chat/send (ModeFull). emit is non-nil only when the host
// asked for streaming and the plugin declared it via ChatFeatures; each call
// sends one chat/delta before the final result.
type Chatter interface {
	Chat(ctx context.Context, p providerproto.ChatParams, emit func(providerproto.ChatDelta) error) (providerproto.ChatResult, error)
}

// ChatFeatures declares what a Chatter supports beyond plain text.
type ChatFeatures interface {
	ChatCapabilities() providerproto.ChatCapabilities
}

// TokenCounter serves tokens/count.
type TokenCounter interface {
	CountTokens(ctx context.Context, p providerproto.TokensCountParams) (int, error)
}

// AuthEnvDeclarer lists env vars the plugin reads for credentials; the host
// passes only these through and treats them as secrets.
type AuthEnvDeclarer interface {
	AuthEnv() []string
}

var providerName = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,31}$`)

// Validate checks the static contract a host will enforce, so a plugin
// fails at startup with a clear message instead of being rejected later.
func Validate(p Provider) error {
	info := p.Info()
	if !providerName.MatchString(info.Name) {
		return fmt.Errorf("providersdk: provider name %q must match %s", info.Name, providerName)
	}
	if info.DisplayName["en"] == "" {
		return fmt.Errorf("providersdk: provider %s needs DisplayName[\"en\"]", info.Name)
	}
	switch p.Mode() {
	case providerproto.ModeDialect:
		d, ok := p.(DialectProvider)
		if !ok {
			return fmt.Errorf("providersdk: dialect mode requires DialectProvider")
		}
		dl := d.Dialect()
		switch dl.API {
		case providerproto.DialectOpenAIChat, providerproto.DialectOpenAIResponses,
			providerproto.DialectAnthropicMessages, providerproto.DialectGemini:
		default:
			return fmt.Errorf("providersdk: unknown dialect API %q", dl.API)
		}
		if dl.BaseURL == "" {
			return fmt.Errorf("providersdk: dialect mode requires a BaseURL")
		}
	case providerproto.ModeFull:
		if _, ok := p.(Chatter); !ok {
			return fmt.Errorf("providersdk: full mode requires Chatter")
		}
	default:
		return fmt.Errorf("providersdk: unknown mode %q", p.Mode())
	}
	return nil
}

// Capabilities derives the announced capabilities from p's interfaces.
func Capabilities(p Provider) providerproto.Capabilities {
	var c providerproto.Capabilities
	if _, ok := p.(Authenticator); ok {
		c.Auth.Login = true
		c.Auth.Token = true
	}
	if _, ok := p.(Logouter); ok {
		c.Auth.Logout = true
	}
	if _, ok := p.(ModelLister); ok {
		c.Models = true
	}
	if _, ok := p.(Chatter); ok {
		if f, ok := p.(ChatFeatures); ok {
			c.Chat = f.ChatCapabilities()
		}
	}
	if _, ok := p.(TokenCounter); ok {
		c.CountTokens = true
	}
	return c
}

// Serve runs the plugin on stdin/stdout until the host sends exit or closes
// stdin. While it runs, os.Stdout points at stderr, so a stray fmt.Println
// in plugin code lands in the host's log instead of corrupting the
// protocol stream.
func Serve(ctx context.Context, p Provider) error {
	out := os.Stdout
	os.Stdout = os.Stderr
	defer func() { os.Stdout = out }()
	return ServeConn(ctx, p, os.Stdin, out)
}

// ServeConn is Serve over arbitrary streams (tests, sockets).
func ServeConn(ctx context.Context, p Provider, r io.Reader, w io.Writer) error {
	if err := Validate(p); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	s := &server{p: p, caps: Capabilities(p), exit: cancel}
	conn := providerproto.NewConn(r, w, s.handle, providerproto.Options{})
	s.conn = conn
	err := conn.Run(ctx)
	if errors.Is(err, context.Canceled) && s.exited() {
		return nil
	}
	return err
}

type server struct {
	p    Provider
	caps providerproto.Capabilities
	conn *providerproto.Conn
	exit context.CancelFunc

	mu          sync.Mutex
	initialized bool
	// shutdownSeq is the arrival order of the shutdown request (0 = none):
	// requests that arrived before it are still served, later ones refused.
	shutdownSeq uint64
	exitCalled  bool
}

func (s *server) exited() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exitCalled
}

type logKey struct{}

// Log sends a $/log notification to the host from inside a handler; level
// is debug, info, warn or error.
func Log(ctx context.Context, level, message string) {
	if conn, ok := ctx.Value(logKey{}).(*providerproto.Conn); ok {
		_ = conn.Notify(ctx, providerproto.MethodLog, providerproto.LogParams{Level: level, Message: message})
	}
}

func decode(raw json.RawMessage, v any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return providerproto.NewError(providerproto.ErrCodeInvalidParams, fmt.Sprintf("invalid params: %v", err))
	}
	return nil
}

func notSupported(method string) error {
	return providerproto.NewError(providerproto.ErrCodeMethodNotFound, fmt.Sprintf("%s is not supported by this plugin", method))
}

// handle adapts dispatch to the wire: results are marshaled here, once.
func (s *server) handle(ctx context.Context, req *providerproto.Request) (json.RawMessage, error) {
	res, err := s.dispatch(context.WithValue(ctx, logKey{}, s.conn), req)
	if err != nil || res == nil {
		return nil, err
	}
	b, err := json.Marshal(res)
	if err != nil {
		return nil, providerproto.NewError(providerproto.ErrCodeInternal, "encode result: "+err.Error())
	}
	return b, nil
}

func (s *server) dispatch(ctx context.Context, req *providerproto.Request) (any, error) {

	seq := providerproto.RequestSeqFromContext(ctx)
	s.mu.Lock()
	initialized := s.initialized
	shutdown := s.shutdownSeq != 0 && seq > s.shutdownSeq
	s.mu.Unlock()

	switch req.Method {
	case providerproto.MethodInitialize:
		return s.initialize(req)
	case providerproto.MethodInitialized:
		return nil, nil
	case providerproto.MethodExit:
		s.mu.Lock()
		s.exitCalled = true
		s.mu.Unlock()
		s.exit()
		return nil, nil
	case providerproto.MethodShutdown:
		s.mu.Lock()
		if s.shutdownSeq == 0 || seq < s.shutdownSeq {
			s.shutdownSeq = seq
		}
		s.mu.Unlock()
		return nil, nil
	}
	if !initialized {
		return nil, providerproto.NewError(providerproto.ErrCodeInvalidRequest, "initialize must come first")
	}
	if shutdown {
		return nil, providerproto.NewError(providerproto.ErrCodeInvalidRequest, "plugin is shutting down")
	}

	switch req.Method {
	case providerproto.MethodAuthStatus, providerproto.MethodAuthToken, providerproto.MethodAuthLogout:
		return s.credentialCall(ctx, req)
	case providerproto.MethodAuthLogin:
		a, ok := s.p.(Authenticator)
		if !ok {
			return nil, notSupported(req.Method)
		}
		var p providerproto.AuthLoginParams
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		return a.Login(ctx, p)
	case providerproto.MethodAuthContinue:
		a, ok := s.p.(Authenticator)
		if !ok {
			return nil, notSupported(req.Method)
		}
		var p providerproto.AuthContinueParams
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		return a.Continue(ctx, p)
	case providerproto.MethodModelsList:
		l, ok := s.p.(ModelLister)
		if !ok {
			return nil, notSupported(req.Method)
		}
		var p providerproto.CredentialParams
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		models, err := l.ListModels(ctx, p.Credential)
		if err != nil {
			return nil, err
		}
		if models == nil {
			models = []providerproto.Model{}
		}
		return providerproto.ModelsListResult{Models: models}, nil
	case providerproto.MethodChatSend:
		return s.chat(ctx, req)
	case providerproto.MethodTokensCount:
		tc, ok := s.p.(TokenCounter)
		if !ok {
			return nil, notSupported(req.Method)
		}
		var p providerproto.TokensCountParams
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		n, err := tc.CountTokens(ctx, p)
		if err != nil {
			return nil, err
		}
		return providerproto.TokensCountResult{Tokens: n}, nil
	}
	return nil, notSupported(req.Method)
}

func (s *server) initialize(req *providerproto.Request) (any, error) {
	var p providerproto.InitializeParams
	if err := decode(req.Params, &p); err != nil {
		return nil, err
	}
	supported := false
	for _, v := range p.ProtocolVersions {
		if v == providerproto.ProtocolVersion {
			supported = true
		}
	}
	if !supported {
		return nil, providerproto.NewError(providerproto.ErrCodeInvalidRequest, fmt.Sprintf(
			"protocol versions %v not supported; this plugin speaks %q", p.ProtocolVersions, providerproto.ProtocolVersion))
	}
	s.mu.Lock()
	s.initialized = true
	s.mu.Unlock()

	res := providerproto.InitializeResult{
		ProtocolVersion: providerproto.ProtocolVersion,
		Provider:        s.p.Info(),
		Mode:            s.p.Mode(),
		Capabilities:    s.caps,
	}
	if d, ok := s.p.(DialectProvider); ok && s.p.Mode() == providerproto.ModeDialect {
		dl := d.Dialect()
		res.Dialect = &dl
	}
	if e, ok := s.p.(AuthEnvDeclarer); ok {
		res.AuthEnv = e.AuthEnv()
	}
	return res, nil
}

func (s *server) credentialCall(ctx context.Context, req *providerproto.Request) (any, error) {
	var p providerproto.CredentialParams
	if err := decode(req.Params, &p); err != nil {
		return nil, err
	}
	if req.Method == providerproto.MethodAuthLogout {
		l, ok := s.p.(Logouter)
		if !ok {
			return nil, notSupported(req.Method)
		}
		return struct{}{}, l.Logout(ctx, p.Credential)
	}
	a, ok := s.p.(Authenticator)
	if !ok {
		return nil, notSupported(req.Method)
	}
	if req.Method == providerproto.MethodAuthStatus {
		return a.AuthStatus(ctx, p.Credential)
	}
	return a.Token(ctx, p.Credential)
}

func (s *server) chat(ctx context.Context, req *providerproto.Request) (any, error) {
	c, ok := s.p.(Chatter)
	if !ok {
		return nil, notSupported(req.Method)
	}
	var p providerproto.ChatParams
	if err := decode(req.Params, &p); err != nil {
		return nil, err
	}
	var emit func(providerproto.ChatDelta) error
	if p.Stream && s.caps.Chat.Stream {
		id := providerproto.RequestIDFromContext(ctx)
		emit = func(d providerproto.ChatDelta) error {
			d.RequestID = id
			return s.conn.Notify(ctx, providerproto.MethodChatDelta, d)
		}
	}
	return c.Chat(ctx, p, emit)
}
