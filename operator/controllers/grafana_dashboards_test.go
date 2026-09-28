/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	// A metric constructor in the controllers: its Name and, for vectors,
	// its label names.
	metricDefRe = regexp.MustCompile(`(?s)prometheus\.New(Counter|Gauge|Histogram|Summary)(Vec)?\(prometheus\.\w+Opts\{.*?Name:\s*"([a-z0-9_]+)".*?\}(, \[\]string\{([^}]*)\})?\)`)
	// An operator metric in a PromQL expression, with its selector if any.
	promMetricRe = regexp.MustCompile(`chatcli_operator_([a-z0-9_]+)(\{([^}]*)\})?`)
	// One matcher inside a selector.
	promMatcherRe = regexp.MustCompile(`([a-z_]+)\s*(=~|!~|!=|=)\s*"([^"]*)"`)
)

// operatorMetrics maps each metric the controllers register (without the
// chatcli_operator_ prefix) to its label names.
func operatorMetrics(t *testing.T) (map[string]map[string]bool, map[string]bool) {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	metrics := map[string]map[string]bool{}
	histograms := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range metricDefRe.FindAllStringSubmatch(string(src), -1) {
			labels := map[string]bool{}
			for _, l := range strings.Split(m[5], ",") {
				if l = strings.Trim(strings.TrimSpace(l), `"`); l != "" {
					labels[l] = true
				}
			}
			metrics[m[3]] = labels
			if m[1] == "Histogram" {
				histograms[m[3]] = true
			}
		}
	}
	if len(metrics) < 20 {
		t.Fatalf("found only %d metric definitions; the scanner is broken", len(metrics))
	}
	return metrics, histograms
}

// sourceLiterals is every string literal of the controllers and the API
// types, the universe label values can come from.
func sourceLiterals(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, pattern := range []string{"*.go", "../api/v1alpha1/*.go"} {
		files, _ := filepath.Glob(pattern)
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			b.Write(src)
		}
	}
	return b.String()
}

// collectExprs walks a dashboard JSON and returns every "expr".
func collectExprs(v any, out *[]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if s, ok := val.(string); ok && k == "expr" {
				*out = append(*out, s)
				continue
			}
			collectExprs(val, out)
		}
	case []any:
		for _, val := range x {
			collectExprs(val, out)
		}
	}
}

// Every Grafana panel queries a metric the operator registers, with labels
// it has and label values the code can emit.
func TestGrafanaDashboardsQueryExportedMetrics(t *testing.T) {
	metrics, histograms := operatorMetrics(t)
	literals := sourceLiterals(t)
	dashboards, err := filepath.Glob("../../deploy/grafana/*.json")
	if err != nil || len(dashboards) == 0 {
		t.Fatalf("no dashboards found (%v)", err)
	}
	for _, path := range dashboards {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var doc any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s is not valid JSON: %v", path, err)
		}
		var exprs []string
		collectExprs(doc, &exprs)
		for _, expr := range exprs {
			for _, m := range promMetricRe.FindAllStringSubmatch(expr, -1) {
				name := m[1]
				base := name
				for _, suffix := range []string{"_bucket", "_sum", "_count"} {
					if trimmed := strings.TrimSuffix(name, suffix); trimmed != name && histograms[trimmed] {
						base = trimmed
					}
				}
				labels, ok := metrics[base]
				if !ok {
					t.Errorf("%s: chatcli_operator_%s is not registered by the operator (%s)", filepath.Base(path), name, expr)
					continue
				}
				for _, mm := range promMatcherRe.FindAllStringSubmatch(m[3], -1) {
					label, op, value := mm[1], mm[2], mm[3]
					if isBucketLabel := label == "le" && base != name; !labels[label] && !isBucketLabel {
						t.Errorf("%s: chatcli_operator_%s has no label %q (%s)", filepath.Base(path), name, label, expr)
						continue
					}
					if op != "=" || strings.HasPrefix(value, "$") {
						continue
					}
					if !strings.Contains(literals, `"`+value+`"`) {
						t.Errorf("%s: %s=%q is never emitted for chatcli_operator_%s (%s)", filepath.Base(path), label, value, name, expr)
					}
				}
			}
		}
	}
}
