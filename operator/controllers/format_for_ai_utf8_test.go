/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// Every FormatForAI block is capped before it joins the LLM prompt. The
// texts are cluster data (service names, log lines, config files) and may
// hold any UTF-8; the cap must not split a character.
func TestFormatForAI_CapsKeepUTF8Valid(t *testing.T) {
	long := strings.Repeat("serviço-日本-", 400)
	cases := map[string]string{
		"blast radius": (&BlastRadiusPrediction{Blockers: []string{long}}).FormatForAI(),
		"cascade": (&CascadeResult{Chain: []CascadeNode{
			{ServiceName: long, Namespace: "ns"}, {ServiceName: long, Namespace: "ns"},
		}}).FormatForAI(),
		"gitops":  (&GitOpsContext{HelmRelease: &HelmReleaseInfo{Name: long, Chart: long}}).FormatForAI(),
		"metrics": (&MetricsSnapshot{Trends: []MetricTrend{{MetricName: long}, {MetricName: long}}}).FormatForAI(),
		"rca":     (&RCAContext{TimeCorrelation: long}).FormatForAI(),
		"source":  (&SourceCodeContext{ConfigContext: []ConfigFileContent{{FilePath: "values.yaml", Content: long}}}).FormatForAI(),
	}
	for name, out := range cases {
		if !strings.HasSuffix(out, "...") {
			t.Errorf("%s: expected the capped form (len %d)", name, len(out))
		}
		if !utf8.ValidString(out) {
			t.Errorf("%s: capped block is not valid UTF-8", name)
		}
	}
}

func TestReadConfigFiles_CapKeepsUTF8Valid(t *testing.T) {
	dir := t.TempDir()
	content := strings.Repeat("a", 1999) + "é" + strings.Repeat("b", 100)
	if err := os.WriteFile(filepath.Join(dir, "values.yaml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got := readConfigFiles(dir, []string{"values.yaml"})
	if len(got) != 1 || !strings.HasSuffix(got[0].Content, "(truncated)") || !utf8.ValidString(got[0].Content) {
		t.Fatalf("config = %+v", got)
	}
}
