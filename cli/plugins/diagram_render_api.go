/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * diagram_render_api.go — a thin exported entry into the @diagram render path so
 * other CLI features (e.g. the /graph knowledge-graph view) can rasterize DOT
 * through the same embedded go-graphviz engine instead of re-implementing it.
 */
package plugins

import "context"

// RenderDOTToFile renders DOT source to an image file using the diagram engine
// (embedded go-graphviz, with the system `dot` as an optional sharper backend
// per CHATCLI_DIAGRAM_BACKEND). format defaults to png and engine to dot when
// empty; dpi defaults to a legible 96 when not positive; output defaults to a
// temp file when empty. Returns a human summary that includes the written path.
func RenderDOTToFile(ctx context.Context, dotSrc, format, engine, output string, dpi int) (string, error) {
	if format == "" {
		format = "png"
	}
	if engine == "" {
		engine = "dot"
	}
	if dpi <= 0 {
		dpi = 96
	}
	cfg := diagramArgs{
		Cmd:     "render",
		DOT:     dotSrc,
		Format:  format,
		Engine:  engine,
		Style:   "dark",
		Backend: configuredDiagramBackend(),
		Output:  output,
		DPI:     dpi,
	}
	cfg, err := finalizeDiagramArgs(cfg)
	if err != nil {
		return "", err
	}
	return diagramRenderAndWrite(ctx, dotSrc, cfg)
}
