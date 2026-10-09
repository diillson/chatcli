/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package evals

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/diillson/chatcli/i18n"
)

// RecordEnv names the file a one-shot run writes its Record to. Unset, the
// one-shot path writes nothing — the variable is the harness's private
// contract with the binary it drives.
const RecordEnv = "CHATCLI_EVAL_RECORD"

// RecordSchema versions the Record layout.
const RecordSchema = 1

// Record is what one chatcli one-shot run reports about itself: the final
// answer, the tools it called and what it cost. The binary writes it at exit
// when RecordEnv is set; the harness reads it to grade the run.
type Record struct {
	Schema       int          `json:"schema"`
	Mode         string       `json:"mode"`
	Provider     string       `json:"provider"`
	Model        string       `json:"model"`
	Final        string       `json:"final"`
	ToolCalls    []ToolCall   `json:"tool_calls,omitempty"`
	Turns        int          `json:"turns"`
	Requests     int          `json:"requests"`
	InputTokens  int64        `json:"input_tokens"`
	OutputTokens int64        `json:"output_tokens"`
	CostUSD      float64      `json:"cost_usd"`
	Error        string       `json:"error,omitempty"`
	Transcript   []Transcript `json:"transcript,omitempty"`
}

// ToolCall is one tool invocation the candidate made.
type ToolCall struct {
	Name string `json:"name"`
	Args string `json:"args,omitempty"`
}

// Transcript is one conversation turn (ShareGPT-style roles).
type Transcript struct {
	From  string `json:"from"`
	Value string `json:"value"`
}

// WriteRecord writes rec to path atomically (temp file + rename), so a
// reader never sees a half-written record.
func WriteRecord(path string, rec *Record) error {
	rec.Schema = RecordSchema
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".eval-record-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ReadRecord loads a Record written by WriteRecord.
func ReadRecord(path string) (*Record, error) {
	data, err := os.ReadFile(path) //#nosec G304 -- path is the harness's own temp file
	if err != nil {
		return nil, err
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("eval record %s: %w", path, err)
	}
	if rec.Schema > RecordSchema {
		return nil, errors.New(i18n.T("evals.record.schema", path, rec.Schema, RecordSchema))
	}
	return &rec, nil
}

// ToolNames returns the distinct tool names in call order.
func (r *Record) ToolNames() []string {
	if r == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, tc := range r.ToolCalls {
		if !seen[tc.Name] {
			seen[tc.Name] = true
			out = append(out, tc.Name)
		}
	}
	return out
}
