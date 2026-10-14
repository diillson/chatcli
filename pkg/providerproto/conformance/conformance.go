/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Package conformance runs a provider plugin binary through the protocol
// contract and reports what passes, fails or could not be exercised. It is
// language-agnostic: it only talks to the process over stdin/stdout, the way
// chatcli does, so plugin authors in any language get the same verdict the
// host would give.
package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/pkg/providerproto"
)

// Status of one check.
type Status string

const (
	Pass Status = "pass"
	Fail Status = "fail"
	Skip Status = "skip"
)

// Check is one verdict.
type Check struct {
	ID     string `json:"id"`
	Status Status `json:"status"`
	// Key is the i18n key of Detail, stable across languages.
	Key      string        `json:"key,omitempty"`
	Detail   string        `json:"detail,omitempty"`
	Duration time.Duration `json:"durationNs"`
}

// Report is the full run.
type Report struct {
	Plugin *providerproto.InitializeResult `json:"plugin,omitempty"`
	Checks []Check                         `json:"checks"`
	// Stderr is the tail of what the plugin logged.
	Stderr string `json:"stderr,omitempty"`
}

// Passed reports whether no check failed.
func (r *Report) Passed() bool {
	for _, c := range r.Checks {
		if c.Status == Fail {
			return false
		}
	}
	return true
}

// Options configures a run.
type Options struct {
	// Command is the plugin executable and its arguments.
	Command []string
	Env     []string
	Dir     string
	// Inputs answers auth/login prompt fields by id.
	Inputs map[string]string
	// Credential skips the login checks and is used as-is.
	Credential providerproto.Credential
	// Model is used for chat checks; default: the first listed model.
	Model string
	// Timeout bounds each check (default 30s).
	Timeout time.Duration
}

const stderrTail = 8 << 10

var providerNamePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,31}$`)

type runner struct {
	opts   Options
	conn   *providerproto.Conn
	report *Report

	protoErrs atomic.Int32
	logs      atomic.Int32

	deltaMu sync.Mutex
	deltas  map[string][]providerproto.ChatDelta

	cred  providerproto.Credential
	model string
}

// Run spawns the plugin, runs every applicable check and stops the process.
// The returned error is about running the kit itself (the binary could not
// start); protocol failures are reported as failed checks.
func Run(ctx context.Context, opts Options) (*Report, error) {
	if len(opts.Command) == 0 {
		return nil, errors.New("conformance: no command")
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	// Details are localized; Init is idempotent, so programmatic callers
	// that never initialized i18n still get readable text.
	i18n.Init()

	// #nosec G204 -- running the plugin under test is the purpose of the kit;
	// the command comes from the person invoking it.
	cmd := exec.CommandContext(ctx, opts.Command[0], opts.Command[1:]...)
	cmd.Dir = opts.Dir
	cmd.Env = append(os.Environ(), opts.Env...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("conformance: stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("conformance: stdout: %w", err)
	}
	stderr := &tailBuffer{max: stderrTail}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("conformance: start %s: %w", opts.Command[0], err)
	}

	r := &runner{
		opts:   opts,
		report: &Report{},
		deltas: map[string][]providerproto.ChatDelta{},
		cred:   opts.Credential,
	}
	r.conn = providerproto.NewConn(stdout, stdin, r.handle, providerproto.Options{
		OnProtocolError: func(error) { r.protoErrs.Add(1) },
	})
	connCtx, stopConn := context.WithCancel(context.Background())
	connDone := make(chan struct{})
	go func() {
		_ = r.conn.Run(connCtx)
		close(connDone)
	}()

	r.runChecks(ctx)
	r.lifecycleExit(cmd, stdin)

	stopConn()
	<-connDone
	r.report.Stderr = stderr.String()
	return r.report, nil
}

func (r *runner) handle(_ context.Context, req *providerproto.Request) (any, error) {
	switch req.Method {
	case providerproto.MethodChatDelta:
		var d providerproto.ChatDelta
		if json.Unmarshal(req.Params, &d) == nil {
			key := fmt.Sprint(d.RequestID)
			r.deltaMu.Lock()
			r.deltas[key] = append(r.deltas[key], d)
			r.deltaMu.Unlock()
		}
	case providerproto.MethodLog:
		r.logs.Add(1)
	}
	return nil, nil
}

// add records a verdict. key is the i18n key of the detail ("" for none);
// Detail carries it rendered in the active language, Key keeps it stable for
// machine readers of the JSON report.
func (r *runner) add(id string, start time.Time, status Status, key string, args ...any) {
	c := Check{ID: id, Status: status, Key: key, Duration: time.Since(start)}
	if key != "" {
		c.Detail = i18n.T(key, args...)
	}
	r.report.Checks = append(r.report.Checks, c)
}

func (r *runner) call(ctx context.Context, method string, params, out any) error {
	ctx, cancel := context.WithTimeout(ctx, r.opts.Timeout)
	defer cancel()
	return r.conn.Call(ctx, method, params, out)
}

func errCode(err error) int {
	var perr *providerproto.Error
	if errors.As(err, &perr) {
		return perr.Code
	}
	return 0
}

func (r *runner) runChecks(ctx context.Context) {
	// lifecycle.before_initialize: nothing but initialize is served first.
	start := time.Now()
	err := r.call(ctx, providerproto.MethodModelsList, providerproto.CredentialParams{}, nil)
	if errCode(err) == providerproto.ErrCodeInvalidRequest {
		r.add("lifecycle.before_initialize", start, Pass, "")
	} else {
		r.add("lifecycle.before_initialize", start, Fail, "provider.check.lifecycle_before_initialize.a_request_before_initialize_must_fail", providerproto.ErrCodeInvalidRequest, err)
	}

	if !r.initialize(ctx) {
		return
	}
	_ = r.conn.Notify(ctx, providerproto.MethodInitialized, struct{}{})

	r.unknownMethod(ctx)
	r.auth(ctx)
	r.models(ctx)
	r.chat(ctx)
	r.stream(ctx)
	r.cancel(ctx)
	r.countTokens(ctx)
}

func (r *runner) initialize(ctx context.Context) bool {
	start := time.Now()
	var res providerproto.InitializeResult
	err := r.call(ctx, providerproto.MethodInitialize, providerproto.InitializeParams{
		ProtocolVersions: []string{providerproto.ProtocolVersion},
		Host:             providerproto.HostInfo{Name: "chatcli-conformance", Version: providerproto.ProtocolVersion},
		Capabilities:     providerproto.HostCapabilities{InteractiveAuth: false},
	}, &res)
	if err != nil {
		r.add("initialize", start, Fail, "provider.check.detail", err)
		return false
	}
	r.report.Plugin = &res

	var problems []string
	if res.ProtocolVersion != providerproto.ProtocolVersion {
		problems = append(problems, i18n.T("provider.check.problem.protocolversion_want", res.ProtocolVersion, providerproto.ProtocolVersion))
	}
	if !providerNamePattern.MatchString(res.Provider.Name) {
		problems = append(problems, i18n.T("provider.check.problem.provider_name_must_match", res.Provider.Name, providerNamePattern))
	} else if d, ok := catalog.LookupProvider(res.Provider.Name); ok && d.Builtin() {
		problems = append(problems, i18n.T("provider.check.problem.provider_name_is_a_built_in", res.Provider.Name))
	}
	if res.Provider.DisplayName["en"] == "" {
		problems = append(problems, i18n.T("provider.check.problem.provider_displayname_en_is_required"))
	}
	switch res.Mode {
	case providerproto.ModeFull:
		if !res.Capabilities.Models {
			problems = append(problems, i18n.T("provider.check.problem.full_mode_should_implement_models_list"))
		}
	case providerproto.ModeDialect:
		if res.Dialect == nil || res.Dialect.BaseURL == "" {
			problems = append(problems, i18n.T("provider.check.problem.dialect_mode_requires_dialect_baseurl"))
		} else if !strings.HasPrefix(res.Dialect.BaseURL, "https://") && !strings.HasPrefix(res.Dialect.BaseURL, "http://") {
			problems = append(problems, i18n.T("provider.check.problem.dialect_baseurl_must_be_an_http"))
		}
		if !res.Capabilities.Auth.Token {
			problems = append(problems, i18n.T("provider.check.problem.dialect_mode_without_auth_token_sends"))
		}
	default:
		problems = append(problems, i18n.T("provider.check.problem.unknown_mode", res.Mode))
	}
	for _, k := range res.AuthEnv {
		if strings.TrimSpace(k) == "" || strings.ContainsAny(k, "= ") {
			problems = append(problems, i18n.T("provider.check.problem.authenv_entry_is_not_an_env", k))
		}
	}
	if len(problems) > 0 {
		r.add("initialize", start, Fail, "provider.check.problems", strings.Join(problems, "; "))
		return true
	}
	r.add("initialize", start, Pass, "provider.check.initialize.mode", res.Provider.Name, res.Provider.Version, res.Mode)
	return true
}

func (r *runner) caps() providerproto.Capabilities { return r.report.Plugin.Capabilities }

func (r *runner) unknownMethod(ctx context.Context) {
	start := time.Now()
	err := r.call(ctx, "chatcli/definitely-not-a-method", nil, nil)
	if errCode(err) == providerproto.ErrCodeMethodNotFound {
		r.add("protocol.unknown_method", start, Pass, "")
		return
	}
	r.add("protocol.unknown_method", start, Fail, "provider.check.protocol_unknown_method.unknown_methods_must_fail_with_got", providerproto.ErrCodeMethodNotFound, err)
}

func (r *runner) auth(ctx context.Context) {
	caps := r.caps()
	if !caps.Auth.Login {
		r.add("auth.login", time.Now(), Skip, "provider.check.auth_login.plugin_declares_no_auth")
	} else if r.cred != nil {
		r.add("auth.login", time.Now(), Skip, "provider.check.auth_login.credential_supplied_by_the_caller")
	} else {
		r.login(ctx)
	}

	if !caps.Auth.Token {
		return
	}
	start := time.Now()
	if r.cred == nil {
		r.add("auth.status", start, Skip, "provider.check.auth_status.no_credential")
		r.add("auth.token", start, Skip, "provider.check.auth_token.no_credential")
		return
	}
	var st providerproto.AuthStatusResult
	if err := r.call(ctx, providerproto.MethodAuthStatus, providerproto.CredentialParams{Credential: r.cred}, &st); err != nil {
		r.add("auth.status", start, Fail, "provider.check.detail", err)
	} else if st.State != providerproto.AuthLoggedIn {
		r.add("auth.status", start, Fail, "provider.check.auth_status.state_with_a_fresh_credential_want", st.State, providerproto.AuthLoggedIn)
	} else {
		r.add("auth.status", start, Pass, "")
	}

	start = time.Now()
	var tok providerproto.AuthTokenResult
	err := r.call(ctx, providerproto.MethodAuthToken, providerproto.CredentialParams{Credential: r.cred}, &tok)
	switch {
	case err != nil:
		r.add("auth.token", start, Fail, "provider.check.detail", err)
	case len(tok.Headers) == 0:
		r.add("auth.token", start, Fail, "provider.check.auth_token.auth_token_returned_no_headers")
	case tok.ExpiresAt != "" && !validTime(tok.ExpiresAt):
		r.add("auth.token", start, Fail, "provider.check.auth_token.expiresat_is_not_rfc_3339", tok.ExpiresAt)
	default:
		if tok.Credential != nil {
			r.cred = tok.Credential
		}
		r.add("auth.token", start, Pass, "provider.check.auth_token.header_s", len(tok.Headers))
	}

	start = time.Now()
	err = r.call(ctx, providerproto.MethodAuthStatus, providerproto.CredentialParams{}, &st)
	if err == nil && st.State == providerproto.AuthLoggedIn {
		r.add("auth.status_without_credential", start, Fail, "provider.check.auth_status_without_credential.reported_logged_in_with_no_credential")
	} else {
		r.add("auth.status_without_credential", start, Pass, "")
	}
}

func validTime(s string) bool {
	_, err := time.Parse(time.RFC3339, s)
	return err == nil
}

// maxLoginSteps bounds the login state machine so a looping plugin fails
// instead of hanging the kit.
const maxLoginSteps = 8

func (r *runner) login(ctx context.Context) {
	start := time.Now()
	var step providerproto.AuthStep
	if err := r.call(ctx, providerproto.MethodAuthLogin, providerproto.AuthLoginParams{Inputs: r.opts.Inputs}, &step); err != nil {
		r.add("auth.login", start, Fail, "provider.check.detail", err)
		return
	}
	for i := 0; i < maxLoginSteps; i++ {
		switch step.Kind {
		case providerproto.StepDone:
			if len(step.Credential) == 0 {
				r.add("auth.login", start, Fail, "provider.check.auth_login.done_step_without_a_credential")
				return
			}
			r.cred = step.Credential
			r.add("auth.login", start, Pass, "provider.check.auth_login.step_s", i+1)
			return
		case providerproto.StepPrompt:
			inputs := map[string]string{}
			var missing []string
			for _, f := range step.Fields {
				if v, ok := r.opts.Inputs[f.ID]; ok {
					inputs[f.ID] = v
				} else if f.Default != "" {
					inputs[f.ID] = f.Default
				} else {
					missing = append(missing, f.ID)
				}
				if f.Label["en"] == "" {
					r.add("auth.login", start, Fail, "provider.check.auth_login.prompt_field_has_no_english_label", f.ID)
					return
				}
			}
			if len(missing) > 0 {
				r.add("auth.login", start, Skip, "provider.check.auth_login.prompt_needs_pass_input_id_value", strings.Join(missing, ", "))
				return
			}
			if step.Session == "" {
				r.add("auth.login", start, Fail, "provider.check.auth_login.prompt_step_without_a_session")
				return
			}
			if err := r.call(ctx, providerproto.MethodAuthContinue, providerproto.AuthContinueParams{Session: step.Session, Inputs: inputs}, &step); err != nil {
				r.add("auth.login", start, Fail, "provider.check.auth_login.auth_continue", err)
				return
			}
		case providerproto.StepOpenURL, providerproto.StepDeviceCode:
			if step.URL == "" {
				r.add("auth.login", start, Fail, "provider.check.auth_login.step_without_a_url", step.Kind)
				return
			}
			r.add("auth.login", start, Skip, "provider.check.auth_login.login_needs_a_person_pass_credential", step.Kind)
			return
		default:
			r.add("auth.login", start, Fail, "provider.check.auth_login.unknown_step_kind", step.Kind)
			return
		}
	}
	r.add("auth.login", start, Fail, "provider.check.auth_login.login_did_not_finish_within_steps", maxLoginSteps)
}

func (r *runner) models(ctx context.Context) {
	start := time.Now()
	if !r.caps().Models {
		r.add("models.list", start, Skip, "provider.check.models_list.plugin_declares_no_models_list")
		r.model = r.opts.Model
		return
	}
	var res providerproto.ModelsListResult
	if err := r.call(ctx, providerproto.MethodModelsList, providerproto.CredentialParams{Credential: r.cred}, &res); err != nil {
		r.add("models.list", start, Fail, "provider.check.detail", err)
		r.model = r.opts.Model
		return
	}
	var problems []string
	seen := map[string]bool{}
	for _, m := range res.Models {
		switch {
		case strings.TrimSpace(m.ID) == "":
			problems = append(problems, i18n.T("provider.check.problem.model_with_an_empty_id"))
		case seen[m.ID]:
			problems = append(problems, i18n.T("provider.check.problem.duplicate_model_id", m.ID))
		}
		seen[m.ID] = true
		if m.ContextWindow < 0 || m.MaxOutputTokens < 0 {
			problems = append(problems, i18n.T("provider.check.problem.negative_token_limits", m.ID))
		}
		if m.MaxOutputTokens > 0 && m.ContextWindow > 0 && m.MaxOutputTokens > m.ContextWindow {
			problems = append(problems, i18n.T("provider.check.problem.maxoutputtokens_exceeds_contextwindow", m.ID))
		}
		if p := m.Pricing; p != nil && (p.Input < 0 || p.Output < 0 || p.CacheRead < 0 || p.CacheWrite < 0) {
			problems = append(problems, i18n.T("provider.check.problem.negative_pricing", m.ID))
		}
		if u := m.Upstream; u != nil {
			if d, ok := catalog.LookupProvider(u.Provider); !ok || !d.Builtin() {
				problems = append(problems, i18n.T("provider.check.problem.upstream_provider_is_not_a_built", m.ID, u.Provider))
			}
		}
	}
	if len(res.Models) == 0 {
		problems = append(problems, i18n.T("provider.check.problem.no_models_listed"))
	}
	if len(problems) > 0 {
		r.add("models.list", start, Fail, "provider.check.problems", strings.Join(problems, "; "))
	} else {
		r.add("models.list", start, Pass, "provider.check.models_list.model_s", len(res.Models))
	}
	r.model = r.opts.Model
	if r.model == "" && len(res.Models) > 0 {
		r.model = res.Models[0].ID
	}
}

func (r *runner) chatParams(stream bool) providerproto.ChatParams {
	return providerproto.ChatParams{
		Credential: r.cred,
		Model:      r.model,
		MaxTokens:  64,
		Stream:     stream,
		Messages: []providerproto.Message{
			{Role: providerproto.RoleSystem, Content: []providerproto.Part{{Type: providerproto.PartText, Text: "You are a test fixture. Answer briefly."}}},
			{Role: providerproto.RoleUser, Content: []providerproto.Part{{Type: providerproto.PartText, Text: "Reply with the single word: pong"}}},
		},
	}
}

func (r *runner) chatReady(id string) bool {
	if r.report.Plugin.Mode != providerproto.ModeFull {
		r.add(id, time.Now(), Skip, "provider.check.chat.skip_dialect")
		return false
	}
	if r.model == "" {
		r.add(id, time.Now(), Skip, "provider.check.chat.skip_no_model")
		return false
	}
	if r.caps().Auth.Login && r.cred == nil {
		r.add(id, time.Now(), Skip, "provider.check.chat.skip_not_logged_in")
		return false
	}
	return true
}

func validateResult(res providerproto.ChatResult) []string {
	var problems []string
	if res.Message.Role != providerproto.RoleAssistant {
		problems = append(problems, i18n.T("provider.check.problem.message_role_want_assistant", res.Message.Role))
	}
	switch res.StopReason {
	case providerproto.StopEnd, providerproto.StopMaxTokens, providerproto.StopToolUse, providerproto.StopRefusal:
	default:
		problems = append(problems, i18n.T("provider.check.problem.stopreason_is_not_one_of_end", res.StopReason))
	}
	if len(res.Message.Content) == 0 && len(res.Message.ToolCalls) == 0 {
		problems = append(problems, i18n.T("provider.check.problem.empty_message"))
	}
	if u := res.Usage; u != nil {
		if u.InputTokens < 0 || u.OutputTokens < 0 || u.CacheReadTokens < 0 || u.CacheWriteTokens < 0 || u.ReasoningTokens < 0 {
			problems = append(problems, i18n.T("provider.check.problem.negative_usage"))
		}
		if u.CostUSD != nil && *u.CostUSD < 0 {
			problems = append(problems, i18n.T("provider.check.problem.negative_costusd"))
		}
	}
	return problems
}

func (r *runner) chat(ctx context.Context) {
	if !r.chatReady("chat.send") {
		return
	}
	start := time.Now()
	var res providerproto.ChatResult
	if err := r.call(ctx, providerproto.MethodChatSend, r.chatParams(false), &res); err != nil {
		r.add("chat.send", start, Fail, "provider.check.detail", err)
		return
	}
	if problems := validateResult(res); len(problems) > 0 {
		r.add("chat.send", start, Fail, "provider.check.problems", strings.Join(problems, "; "))
		return
	}
	if res.Usage != nil {
		r.add("chat.send", start, Pass, "provider.check.chat_send.usage", res.Usage.InputTokens, res.Usage.OutputTokens)
	} else {
		r.add("chat.send", start, Pass, "provider.check.chat_send.no_usage")
	}

	start = time.Now()
	bad := r.chatParams(false)
	bad.Model = "chatcli-conformance-no-such-model"
	err := r.call(ctx, providerproto.MethodChatSend, bad, nil)
	if errCode(err) == providerproto.ErrCodeModelNotFound {
		r.add("chat.unknown_model", start, Pass, "")
	} else {
		r.add("chat.unknown_model", start, Fail, "provider.check.chat_unknown_model.an_unknown_model_must_fail_with", providerproto.ErrCodeModelNotFound, err)
	}
}

func (r *runner) stream(ctx context.Context) {
	if !r.caps().Chat.Stream {
		r.add("chat.stream", time.Now(), Skip, "provider.check.chat_stream.plugin_declares_no_streaming")
		return
	}
	if !r.chatReady("chat.stream") {
		return
	}
	start := time.Now()
	before := r.deltaKeys()
	var res providerproto.ChatResult
	if err := r.call(ctx, providerproto.MethodChatSend, r.chatParams(true), &res); err != nil {
		r.add("chat.stream", start, Fail, "provider.check.detail", err)
		return
	}
	got := r.newDeltas(before)
	if len(got) == 0 {
		r.add("chat.stream", start, Fail, "provider.check.chat_stream.no_chat_delta_notification_arrived_before")
		return
	}
	var text strings.Builder
	for _, d := range got {
		text.WriteString(d.Text)
	}
	final := partsText(res.Message.Content)
	if problems := validateResult(res); len(problems) > 0 {
		r.add("chat.stream", start, Fail, "provider.check.problems", strings.Join(problems, "; "))
		return
	}
	if final != "" && text.String() != final {
		r.add("chat.stream", start, Fail, "provider.check.chat_stream.deltas_spell_but_the_final_text", text.String(), final)
		return
	}
	r.add("chat.stream", start, Pass, "provider.check.chat_stream.delta_s", len(got))
}

func partsText(parts []providerproto.Part) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Type == providerproto.PartText {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

func (r *runner) deltaKeys() map[string]int {
	r.deltaMu.Lock()
	defer r.deltaMu.Unlock()
	out := make(map[string]int, len(r.deltas))
	for k, v := range r.deltas {
		out[k] = len(v)
	}
	return out
}

// newDeltas returns the deltas of request streams that appeared since
// before (exactly one stream is expected per check).
func (r *runner) newDeltas(before map[string]int) []providerproto.ChatDelta {
	r.deltaMu.Lock()
	defer r.deltaMu.Unlock()
	var out []providerproto.ChatDelta
	for k, v := range r.deltas {
		if _, seen := before[k]; !seen {
			out = append(out, v...)
		}
	}
	return out
}

// cancel abandons a streaming chat mid-flight and checks the plugin keeps
// serving afterwards; a plugin that ignores $/cancelRequest wastes the
// user's quota, one that crashes on it kills the session.
func (r *runner) cancel(ctx context.Context) {
	if !r.caps().Chat.Stream {
		r.add("chat.cancel", time.Now(), Skip, "provider.check.chat_cancel.plugin_declares_no_streaming")
		return
	}
	if !r.chatReady("chat.cancel") {
		return
	}
	start := time.Now()
	callCtx, cancel := context.WithCancel(ctx)
	errc := make(chan error, 1)
	before := r.deltaKeys()
	go func() { errc <- r.conn.Call(callCtx, providerproto.MethodChatSend, r.chatParams(true), nil) }()

	deadline := time.After(r.opts.Timeout)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
wait:
	for {
		select {
		case <-tick.C:
			if len(r.newDeltas(before)) > 0 {
				break wait
			}
		case err := <-errc:
			// Finished before the first delta could be observed: nothing to
			// cancel; still a pass for liveness below.
			cancel()
			if err != nil {
				r.add("chat.cancel", start, Fail, "provider.check.chat_cancel.stream_failed_before_cancellation", err)
				return
			}
			r.add("chat.cancel", start, Skip, "provider.check.chat_cancel.stream_finished_before_it_could_be")
			return
		case <-deadline:
			cancel()
			r.add("chat.cancel", start, Fail, "provider.check.chat_cancel.no_delta_within", r.opts.Timeout)
			return
		}
	}
	cancel()
	<-errc

	var res providerproto.ChatResult
	if err := r.call(ctx, providerproto.MethodChatSend, r.chatParams(false), &res); err != nil {
		r.add("chat.cancel", start, Fail, "provider.check.chat_cancel.plugin_stopped_serving_after_a_cancelled", err)
		return
	}
	r.add("chat.cancel", start, Pass, "")
}

func (r *runner) countTokens(ctx context.Context) {
	if !r.caps().CountTokens {
		r.add("tokens.count", time.Now(), Skip, "provider.check.tokens_count.plugin_declares_no_tokens_count")
		return
	}
	start := time.Now()
	p := r.chatParams(false)
	var res providerproto.TokensCountResult
	err := r.call(ctx, providerproto.MethodTokensCount, providerproto.TokensCountParams{
		Credential: r.cred, Model: r.model, Messages: p.Messages,
	}, &res)
	switch {
	case err != nil:
		r.add("tokens.count", start, Fail, "provider.check.detail", err)
	case res.Tokens <= 0:
		r.add("tokens.count", start, Fail, "provider.check.tokens_count.count_for_a_non_empty_prompt", res.Tokens)
	default:
		r.add("tokens.count", start, Pass, "provider.check.tokens_count.token_s", res.Tokens)
	}
}

// lifecycleExit runs shutdown → exit and requires a clean, timely exit with
// nothing but protocol messages on stdout.
func (r *runner) lifecycleExit(cmd *exec.Cmd, stdin io.Closer) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), r.opts.Timeout)
	defer cancel()
	shutdownErr := r.conn.Call(ctx, providerproto.MethodShutdown, nil, nil)
	_ = r.conn.Notify(ctx, providerproto.MethodExit, nil)

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-exited:
	case <-time.After(5 * time.Second):
		_ = stdin.Close()
		select {
		case waitErr = <-exited:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			waitErr = <-exited
			r.add("lifecycle.exit", start, Fail, "provider.check.lifecycle_exit.process_ignored_exit_and_closed_stdin")
			r.stdoutCheck()
			return
		}
	}
	switch {
	case shutdownErr != nil:
		r.add("lifecycle.exit", start, Fail, "provider.check.lifecycle_exit.shutdown", shutdownErr)
	case waitErr != nil:
		r.add("lifecycle.exit", start, Fail, "provider.check.lifecycle_exit.exit_status", waitErr)
	default:
		r.add("lifecycle.exit", start, Pass, "")
	}
	r.stdoutCheck()
}

func (r *runner) stdoutCheck() {
	if n := r.protoErrs.Load(); n > 0 {
		r.add("protocol.stdout_clean", time.Now(), Fail, "provider.check.protocol_stdout_clean.non_protocol_line_s_on_stdout", n)
		return
	}
	r.add("protocol.stdout_clean", time.Now(), Pass, "")
}

// tailBuffer keeps the last max bytes written.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf bytes.Buffer
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf.Write(p)
	if over := t.buf.Len() - t.max; over > 0 {
		t.buf.Next(over)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buf.String()
}
