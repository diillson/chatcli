/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package ctxmgr

import (
	"fmt"
	"regexp"
	"strings"

	"go.uber.org/zap"
)

const (
	MaxContextNameLength = 64
	MaxTotalSizeBytes    = 200 * 1024 * 1024 // 200MB
	MinNameLength        = 3
	MaxDescriptionLength = 500
)

// Validator valida contextos e suas operações
type Validator struct {
	logger *zap.Logger
}

// NewValidator cria uma nova instância de Validator
func NewValidator(logger *zap.Logger) *Validator {
	return &Validator{
		logger: logger,
	}
}

// ValidateName valida o nome de um contexto
func (v *Validator) ValidateName(name string) error {
	name = strings.TrimSpace(name)

	if len(name) < MinNameLength {
		return fmt.Errorf("nome muito curto (mínimo %d caracteres)", MinNameLength)
	}

	if len(name) > MaxContextNameLength {
		return fmt.Errorf("nome muito longo (máximo %d caracteres)", MaxContextNameLength)
	}

	// Validar caracteres permitidos: letras, números, hífens, underscores, espaços
	validNamePattern := regexp.MustCompile(`^[a-zA-Z0-9\-_ ]+$`)
	if !validNamePattern.MatchString(name) {
		return fmt.Errorf("nome contém caracteres inválidos (apenas letras, números, hífens, underscores e espaços)")
	}

	// Não permitir apenas espaços/hífens
	if strings.Trim(name, " -_") == "" {
		return fmt.Errorf("nome inválido: deve conter pelo menos uma letra ou número")
	}

	return nil
}

// ValidateDescription valida a descrição de um contexto
func (v *Validator) ValidateDescription(description string) error {
	if len(description) > MaxDescriptionLength {
		return fmt.Errorf("descrição muito longa (máximo %d caracteres)", MaxDescriptionLength)
	}
	return nil
}

// ValidateTotalSize valida o tamanho total de arquivos
func (v *Validator) ValidateTotalSize(size int64) error {
	if size <= 0 {
		return fmt.Errorf("tamanho total inválido: %d bytes", size)
	}

	if size > MaxTotalSizeBytes {
		return fmt.Errorf("tamanho total excede o limite (%.2f MB / %.2f MB)",
			float64(size)/1024/1024,
			float64(MaxTotalSizeBytes)/1024/1024)
	}

	return nil
}

// ValidateTags valida as tags de um contexto
func (v *Validator) ValidateTags(tags []string) error {
	if len(tags) > 10 {
		return fmt.Errorf("número máximo de tags excedido (máximo 10)")
	}

	tagPattern := regexp.MustCompile(`^[a-zA-Z0-9\-_]+$`)

	for _, tag := range tags {
		tag = strings.TrimSpace(tag)

		if len(tag) < 2 {
			return fmt.Errorf("tag '%s' muito curta (mínimo 2 caracteres)", tag)
		}

		if len(tag) > 20 {
			return fmt.Errorf("tag '%s' muito longa (máximo 20 caracteres)", tag)
		}

		if !tagPattern.MatchString(tag) {
			return fmt.Errorf("tag '%s' contém caracteres inválidos", tag)
		}
	}

	return nil
}

// ValidateMode valida o modo de processamento
func (v *Validator) ValidateMode(mode ProcessingMode) error {
	validModes := map[ProcessingMode]bool{
		ModeFull:    true,
		ModeSummary: true,
		ModeChunked: true,
		ModeSmart:   true,
	}

	if !validModes[mode] {
		return fmt.Errorf("modo de processamento inválido: '%s'", mode)
	}

	return nil
}

// ValidateContext valida um contexto completo
func (v *Validator) ValidateContext(ctx *FileContext) *ValidationResult {
	result := &ValidationResult{
		Valid:    true,
		Errors:   make([]string, 0),
		Warnings: make([]string, 0),
	}

	// Validar nome
	if err := v.ValidateName(ctx.Name); err != nil {
		result.Valid = false
		result.Errors = append(result.Errors, err.Error())
	}

	// Validar descrição
	if ctx.Description != "" {
		if err := v.ValidateDescription(ctx.Description); err != nil {
			result.Valid = false
			result.Errors = append(result.Errors, err.Error())
		}
	}

	// Validar tamanho
	if err := v.ValidateTotalSize(ctx.TotalSize); err != nil {
		result.Valid = false
		result.Errors = append(result.Errors, err.Error())
	}

	// Validar número de arquivos
	if ctx.FileCount == 0 {
		result.Valid = false
		result.Errors = append(result.Errors, "contexto não contém arquivos")
	}

	// Validar modo
	if err := v.ValidateMode(ctx.Mode); err != nil {
		result.Valid = false
		result.Errors = append(result.Errors, err.Error())
	}

	// Validar tags
	if len(ctx.Tags) > 0 {
		if err := v.ValidateTags(ctx.Tags); err != nil {
			result.Valid = false
			result.Errors = append(result.Errors, err.Error())
		}
	}

	// Warnings
	if ctx.FileCount > 500 {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("número alto de arquivos (%d) pode afetar performance", ctx.FileCount))
	}

	if ctx.TotalSize > 50*1024*1024 {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("tamanho grande (%.2f MB) pode consumir muitos tokens",
				float64(ctx.TotalSize)/1024/1024))
	}

	// Calcular uso estimado de tokens
	estimatedTokens := int(ctx.TotalSize / 4) // ~4 chars por token
	if estimatedTokens > 50000 {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("uso estimado de ~%d tokens pode exceder limites de alguns modelos",
				estimatedTokens))
	}

	return result
}

// ValidatePriority valida a prioridade de anexação
func (v *Validator) ValidatePriority(priority int) error {
	if priority < 0 {
		return fmt.Errorf("prioridade não pode ser negativa")
	}

	if priority > 1000 {
		return fmt.Errorf("prioridade muito alta (máximo 1000)")
	}

	return nil
}
