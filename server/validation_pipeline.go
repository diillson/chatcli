/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package server

import (
	"fmt"

	pb "github.com/diillson/chatcli/proto/chatcli/v1"
)

// Pipeline request limits. Tasks and tool arguments share the prompt bound;
// quality overrides are a handful of CHATCLI_QUALITY_* keys.
const (
	maxPipelineToolNameLen  = 128
	maxPipelineQualityKeys  = 16
	maxPipelineQualityValue = 256
)

func validateChatTurn(req interface{}) error {
	r, ok := req.(*pb.ChatTurnRequest)
	if !ok {
		return nil
	}
	if err := validateBoundedString("session", r.Session, pipelineSessionMaxLen); err != nil {
		return err
	}
	if err := validateBoundedString("text", r.Text, maxPromptBytes); err != nil {
		return err
	}
	if err := validateBoundedString("provider", r.Provider, maxSessionNameLen); err != nil {
		return err
	}
	return validateBoundedString("model", r.Model, maxSessionNameLen)
}

func validatePipelineTask(req interface{}) error {
	r, ok := req.(*pb.PipelineTaskRequest)
	if !ok {
		return nil
	}
	if err := validateBoundedString("session", r.Session, pipelineSessionMaxLen); err != nil {
		return err
	}
	if err := validateBoundedString("task", r.Task, maxPromptBytes); err != nil {
		return err
	}
	if err := validateBoundedString("provider", r.Provider, maxSessionNameLen); err != nil {
		return err
	}
	if err := validateBoundedString("model", r.Model, maxSessionNameLen); err != nil {
		return err
	}
	if len(r.Quality) > maxPipelineQualityKeys {
		return fmt.Errorf("quality carries more than %d entries", maxPipelineQualityKeys)
	}
	for k, v := range r.Quality {
		if len(k) > maxSessionNameLen {
			return fmt.Errorf("quality key exceeds maximum length of %d characters", maxSessionNameLen)
		}
		if len(v) > maxPipelineQualityValue {
			return fmt.Errorf("quality value for %q exceeds maximum size of %d bytes", k, maxPipelineQualityValue)
		}
	}
	return nil
}

func validateRunPipelineTool(req interface{}) error {
	r, ok := req.(*pb.RunPipelineToolRequest)
	if !ok {
		return nil
	}
	if err := validateBoundedString("name", r.Name, maxPipelineToolNameLen); err != nil {
		return err
	}
	return validateBoundedString("args", r.Args, maxPromptBytes)
}
