/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package imagegen

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/pkg/pulse/pulsetest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pulseFakeEditor generates and edits fixed images.
type pulseFakeEditor struct {
	err error
}

func (*pulseFakeEditor) Name() string  { return "command:/opt/secret/bin/imagegen" }
func (*pulseFakeEditor) Model() string { return "fake-image-1" }
func (f *pulseFakeEditor) Generate(_ context.Context, _ string, opts Options) ([]Image, error) {
	if f.err != nil {
		return nil, f.err
	}
	return []Image{{Data: []byte("12345"), Mime: "image/png"}, {Data: []byte("678"), Mime: "image/png"}}, nil
}
func (f *pulseFakeEditor) Edit(_ context.Context, _ string, _ []Image, _ EditOptions) ([]Image, error) {
	return []Image{{Data: []byte("abcdefgh"), Mime: "image/png"}}, nil
}

func TestGenerateTracedShowsOnDashboard(t *testing.T) {
	rec := pulsetest.Record(t)
	ctx := pulse.WithParent(context.Background(), "tool-7")
	imgs, err := GenerateTraced(ctx, &pulseFakeEditor{}, "SECRET-PROMPT", Options{Size: "1024x768", N: 2})
	require.NoError(t, err)
	require.Len(t, imgs, 2)

	ev := rec.WaitFor(t, "generate end", pulsetest.Is(pulse.KindTool, pulseGenerate, pulse.PhaseEnd))
	assert.Equal(t, "tool-7", ev.Parent)
	assert.Equal(t, pulse.StatusOK, ev.Status)
	assert.Equal(t, map[string]string{
		"provider": "command", "model": "fake-image-1", "engine": "local", "size": "1024x768",
		"requested": "2", "images": "2", "bytes_out": "8",
	}, ev.Attrs)
	for _, e := range rec.Events() {
		for _, v := range e.Attrs {
			assert.False(t, strings.Contains(v, "SECRET") || strings.Contains(v, "/opt"), "leak: %q", v)
		}
	}
}

func TestEditTracedShowsOnDashboard(t *testing.T) {
	rec := pulsetest.Record(t)
	in := []Image{{Data: []byte("0123456789")}}
	_, err := EditTraced(context.Background(), &pulseFakeEditor{}, "SECRET-PROMPT", in,
		EditOptions{Size: "free text size", Mask: []byte{1}})
	require.NoError(t, err)

	ev := rec.WaitFor(t, "edit end", pulsetest.Is(pulse.KindTool, pulseEdit, pulse.PhaseEnd))
	assert.Equal(t, "1", ev.Attrs["images_in"])
	assert.Equal(t, "10", ev.Attrs["bytes_in"])
	assert.Equal(t, "8", ev.Attrs["bytes_out"])
	assert.Equal(t, "true", ev.Attrs["mask"])
	assert.Empty(t, ev.Attrs["size"], "a size that is not WxH must not travel")
}

func TestGenerateTracedReportsFailure(t *testing.T) {
	rec := pulsetest.Record(t)
	_, err := GenerateTraced(context.Background(), &pulseFakeEditor{err: errors.New("SECRET-ERROR")}, "p", Options{})
	require.Error(t, err)
	ev := rec.WaitFor(t, "failed generate", pulsetest.Is(pulse.KindTool, pulseGenerate, pulse.PhaseEnd))
	assert.Equal(t, pulse.StatusError, ev.Status)
	assert.Equal(t, "0", ev.Attrs["images"])
}
