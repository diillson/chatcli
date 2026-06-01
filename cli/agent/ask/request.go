/*
 * ChatCLI - AskUser request/answer types and parsing.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 *
 * The @ask / ask_user tool lets the LLM put a small multiple-choice decision
 * back to the human: 1-6 questions, each with a header, a set of options
 * (label + description), single OR multi-select, plus an implicit free-text
 * "Other" choice. This leaf package holds only the data types, the args
 * parser/validator, the native-tool JSON schema, and the result formatters.
 *
 * It deliberately imports nothing from the agent loop, the palette, or the
 * plugin manager so it can be shared by all three without an import cycle
 * (mirrors cli/agent/park).
 */
package ask

import (
	"encoding/json"
	"fmt"
	"strings"
)

// MaxQuestions caps how many questions a single @ask call may bundle. Kept
// small so the overlay stays scannable. Applies to every mode (chat/agent/coder).
const MaxQuestions = 6

// MaxOptions caps the options per question so the list fits the overlay window.
const MaxOptions = 8

// Option is one selectable choice within a question.
type Option struct {
	Label string `json:"label"`
	Desc  string `json:"description,omitempty"`
}

// Question is a single prompt presented to the user.
type Question struct {
	Header      string   `json:"header"`
	Question    string   `json:"question"`
	MultiSelect bool     `json:"multiSelect,omitempty"`
	Options     []Option `json:"options"`
}

// Answer is the resolved selection for one question. Selected holds the chosen
// option labels (one for single-select, N for multi-select); Other holds the
// free-text the user typed when they picked the "Other" row (empty otherwise).
type Answer struct {
	Header   string   `json:"header"`
	Selected []string `json:"selected"`
	Other    string   `json:"other,omitempty"`
}

// envelope is the accepted args shape: {"questions":[ ... ]}.
type envelope struct {
	Questions []Question `json:"questions"`
}

// ParseRequest decodes the tool args (a single JSON string) into validated
// questions. It accepts either the {"questions":[...]} envelope or a bare
// array of questions, so a stray model that forgets the wrapper still works.
func ParseRequest(argsJSON string) ([]Question, error) {
	s := strings.TrimSpace(argsJSON)
	if s == "" {
		return nil, fmt.Errorf("ask: empty args; expected {\"questions\":[{\"header\":...,\"question\":...,\"options\":[...]}]}")
	}

	var questions []Question
	switch s[0] {
	case '{':
		var env envelope
		if err := json.Unmarshal([]byte(s), &env); err != nil {
			return nil, fmt.Errorf("ask: parse envelope: %w", err)
		}
		questions = env.Questions
	case '[':
		if err := json.Unmarshal([]byte(s), &questions); err != nil {
			return nil, fmt.Errorf("ask: parse questions array: %w", err)
		}
	default:
		return nil, fmt.Errorf("ask: args must be a JSON object or array, got %q", s[:1])
	}

	if err := validate(questions); err != nil {
		return nil, err
	}
	return questions, nil
}

func validate(qs []Question) error {
	if len(qs) == 0 {
		return fmt.Errorf("ask: at least one question is required")
	}
	if len(qs) > MaxQuestions {
		return fmt.Errorf("ask: too many questions (%d); max is %d", len(qs), MaxQuestions)
	}
	for i := range qs {
		q := &qs[i]
		if strings.TrimSpace(q.Header) == "" {
			return fmt.Errorf("ask: question %d: header is required", i+1)
		}
		if strings.TrimSpace(q.Question) == "" {
			return fmt.Errorf("ask: question %d (%s): question text is required", i+1, q.Header)
		}
		if len(q.Options) == 0 {
			return fmt.Errorf("ask: question %d (%s): at least one option is required", i+1, q.Header)
		}
		if len(q.Options) > MaxOptions {
			return fmt.Errorf("ask: question %d (%s): too many options (%d); max is %d", i+1, q.Header, len(q.Options), MaxOptions)
		}
		for j := range q.Options {
			if strings.TrimSpace(q.Options[j].Label) == "" {
				return fmt.Errorf("ask: question %d (%s): option %d has an empty label", i+1, q.Header, j+1)
			}
		}
	}
	return nil
}
