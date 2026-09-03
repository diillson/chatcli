/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Labeled retrieval evaluation that runs in CI. The samples are
 * synthetic (no personal data) but shaped like real use: short facts and
 * documentation passages, queries that paraphrase them, and distractors
 * that share vocabulary. The thresholds are floors — a change that drops
 * recall or MRR under them is a regression, not a tuning choice.
 */
package eval

import (
	"context"
	"fmt"
	"testing"

	"github.com/diillson/chatcli/cli/ctxmgr"
	"github.com/diillson/chatcli/cli/workspace/memory"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

type labeledFact struct {
	key, content, category string
}

var labeledFacts = []labeledFact{
	{"pgbouncer", "The billing API talks to Postgres through pgbouncer with a pool of 40 connections", "architecture"},
	{"redis", "Session tokens live in Redis with a 12 hour TTL and allkeys-lru eviction", "architecture"},
	{"deploy", "Production deploys go through the blue-green pipeline and need a manual approval step", "process"},
	{"lint", "golangci-lint must pass before any pull request is merged", "process"},
	{"i18n", "Every user-facing string goes through the i18n layer with English and Portuguese keys", "pattern"},
	{"retry", "HTTP clients retry idempotent calls three times with exponential backoff", "pattern"},
	{"windows", "The embedded filesystem requires forward slashes even on Windows builds", "gotcha"},
	{"timezone", "Cron schedules are evaluated in UTC, not the machine timezone", "gotcha"},
	{"otel", "Traces are exported through OpenTelemetry to the collector on port 4317", "architecture"},
	{"webhook", "Payment webhooks are verified with an HMAC signature before processing", "architecture"},
	{"cache", "The catalog cache refreshes every 15 minutes and falls back to the embedded snapshot", "architecture"},
	{"flags", "Feature flags are read from the config service with a local override file", "pattern"},
}

var factQueries = []struct {
	query string
	want  []string
}{
	{"how does billing connect to postgres", []string{"pgbouncer"}},
	{"where are session tokens stored and for how long", []string{"redis"}},
	{"what is required before a production deploy", []string{"deploy"}},
	{"which linter gates pull requests", []string{"lint"}},
	{"how are user facing strings translated", []string{"i18n"}},
	{"retry policy for http calls", []string{"retry"}},
	{"path separators on windows for the embedded filesystem", []string{"windows"}},
	{"which timezone do cron schedules use", []string{"timezone"}},
	{"how are traces exported", []string{"otel"}},
	{"how are payment webhooks verified", []string{"webhook"}},
	{"how often does the catalog cache refresh", []string{"cache"}},
	{"where do feature flags come from", []string{"flags"}},
}

func TestLabeled_FactRecallFloors(t *testing.T) {
	fi := memory.NewFactIndex(t.TempDir(), memory.DefaultConfig(), zap.NewNop())
	for _, f := range labeledFacts {
		if !fi.AddFact(f.content, f.category, nil) {
			t.Fatalf("add %s", f.key)
		}
	}
	idByKey := map[string]string{}
	for _, f := range fi.GetAll() {
		for _, lf := range labeledFacts {
			if f.Content == lf.content {
				idByKey[lf.key] = f.ID
			}
		}
	}
	samples := make([]Sample, 0, len(factQueries))
	for _, q := range factQueries {
		rel := make([]string, 0, len(q.want))
		for _, k := range q.want {
			rel = append(rel, idByKey[k])
		}
		samples = append(samples, Sample{Query: q.query, Relevant: rel})
	}
	rank := func(query string, k int) []string {
		facts := fi.SearchBlendedMin(memory.ExtractKeywords([]string{query}), nil, memory.DefaultRankWeights(), 0)
		out := make([]string, 0, k)
		for i, f := range facts {
			if i >= k {
				break
			}
			out = append(out, f.ID)
		}
		return out
	}
	m := Evaluate(rank, samples, 3)
	t.Logf("facts: n=%d recall@3=%.2f mrr=%.2f ndcg=%.2f", m.N, m.RecallAtK, m.MRR, m.NDCGAtK)
	if m.N != len(samples) {
		t.Fatalf("every sample must be scored, got %d/%d", m.N, len(samples))
	}
	if m.RecallAtK < 0.9 {
		t.Fatalf("fact recall@3 regressed: %.2f < 0.90", m.RecallAtK)
	}
	if m.MRR < 0.75 {
		t.Fatalf("fact MRR regressed: %.2f < 0.75", m.MRR)
	}
}

func TestLabeled_KnowledgeRecallFloors(t *testing.T) {
	docs := []struct{ path, text string }{
		{"auth.md", "# Authentication\nLogin uses OAuth device flow. Refresh tokens rotate on every use and are stored encrypted at rest.\n"},
		{"billing.md", "# Billing\nInvoices are generated nightly. Failed card charges retry after 24 hours and again after 72 hours.\n"},
		{"deploy.md", "# Deploy\nThe blue-green pipeline promotes the staging image after the smoke suite passes. Rollback swaps the load balancer target.\n"},
		{"observability.md", "# Observability\nDashboards read from Prometheus. Alerts page the on-call engineer when error rate exceeds two percent for five minutes.\n"},
		{"storage.md", "# Storage\nUploads land in object storage under a per-tenant prefix with server-side encryption and a 90 day lifecycle rule.\n"},
		{"search.md", "# Search\nThe search index is rebuilt incrementally: only documents whose checksum changed are re-embedded.\n"},
		{"email.md", "# Email\nTransactional email goes through the provider webhook; bounces mark the address as undeliverable.\n"},
		{"rate.md", "# Rate limiting\nPublic endpoints allow 600 requests per minute per API key; bursts use a token bucket.\n"},
	}
	files := make([]utils.FileInfo, 0, len(docs))
	for _, d := range docs {
		files = append(files, utils.FileInfo{Path: d.path, Type: ".md", Content: d.text})
	}
	fc := &ctxmgr.FileContext{ID: "kb", Name: "kb", Files: files, Metadata: map[string]string{"segmenter": "v2"}}
	engine := ctxmgr.NewRetrievalEngine(nil, t.TempDir(), zap.NewNop())

	queries := []struct{ query, path string }{
		{"how do refresh tokens rotate", "auth.md"},
		{"when are failed card charges retried", "billing.md"},
		{"how does rollback work in the deploy pipeline", "deploy.md"},
		{"when does the on-call engineer get paged", "observability.md"},
		{"upload lifecycle rule and encryption", "storage.md"},
		{"which documents get re-embedded", "search.md"},
		{"what happens to bounced email addresses", "email.md"},
		{"requests per minute per api key", "rate.md"},
	}
	samples := make([]Sample, 0, len(queries))
	for _, q := range queries {
		samples = append(samples, Sample{Query: q.query, Relevant: []string{q.path}})
	}
	rank := func(query string, k int) []string {
		segs, err := engine.RetrieveHybrid(context.Background(), fc, query, k)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, 0, len(segs))
		seen := map[string]bool{}
		for _, s := range segs {
			if !seen[s.FilePath] {
				seen[s.FilePath] = true
				out = append(out, s.FilePath)
			}
		}
		return out
	}
	m := Evaluate(rank, samples, 2)
	t.Logf("knowledge: n=%d recall@2=%.2f mrr=%.2f", m.N, m.RecallAtK, m.MRR)
	if m.RecallAtK < 0.9 || m.MRR < 0.8 {
		t.Fatalf("knowledge retrieval regressed: recall@2=%.2f mrr=%.2f", m.RecallAtK, m.MRR)
	}

	// The keyless MMR reranker must not cost recall on this corpus.
	engine.SetReranker(ctxmgr.MMRReranker{Lambda: 0.7})
	mm := Evaluate(rank, samples, 2)
	if mm.RecallAtK < m.RecallAtK-1e-9 {
		t.Fatalf("MMR must not reduce recall: %.2f < %.2f", mm.RecallAtK, m.RecallAtK)
	}
	_ = fmt.Sprintf
}
