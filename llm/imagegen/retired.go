/*
 * ChatCLI - Provider for an image model past its shutdown date.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The factory returns it when CHATCLI_IMAGE_MODEL (or the edit model) names a
 * model the catalog knows has been shut down. The provider would reject the
 * request with an opaque error; this says which model, since when, and what
 * replaces it, before any request leaves the machine.
 */
package imagegen

import (
	"context"
	"fmt"
	"strings"
)

// retiredModel is the backend for a configured model that has been shut down.
type retiredModel struct {
	info ModelInfo
}

// retiredOrNil returns the retired-model backend when model is a catalog
// entry whose shutdown date has arrived, nil otherwise.
func retiredOrNil(model string) Provider {
	if strings.TrimSpace(model) == "" {
		return nil
	}
	m, retired, ok := LookupModel(model)
	if !ok || !retired {
		return nil
	}
	return &retiredModel{info: m}
}

func (r *retiredModel) err() error {
	msg := fmt.Sprintf("imagegen: %s was shut down on %s", r.info.Name, r.info.Retires)
	if note := strings.TrimSpace(r.info.Note); note != "" {
		msg += " (" + strings.TrimSuffix(note, ".") + ")"
	}
	return fmt.Errorf("%s; @image models lists the current ones", msg)
}

// Name is the provider the retired model belonged to, so /config still says
// where the configuration points.
func (r *retiredModel) Name() string { return r.info.Provider }

// Model returns the retired model id.
func (r *retiredModel) Model() string { return r.info.Name }

// Generate refuses with the shutdown date.
func (r *retiredModel) Generate(_ context.Context, _ string, _ Options) ([]Image, error) {
	return nil, r.err()
}

// Edit refuses with the shutdown date.
func (r *retiredModel) Edit(_ context.Context, _ string, _ []Image, _ EditOptions) ([]Image, error) {
	return nil, r.err()
}
