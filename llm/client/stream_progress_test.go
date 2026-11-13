/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package client

import (
	"context"
	"net/http"
	"testing"
)

func TestStreamProgressReachesTheSinkInContext(t *testing.T) {
	var got []int
	ctx := WithStreamProgress(context.Background(), func(chars int) { got = append(got, chars) })
	ReportStreamProgress(ctx, 12)
	ReportStreamProgress(ctx, 0)  // an empty piece is not progress
	ReportStreamProgress(ctx, -3) // nor is a bogus size
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://example.invalid", nil)
	if err != nil {
		t.Fatal(err)
	}
	ReportResponseProgress(&http.Response{Request: req}, 5)
	if len(got) != 2 || got[0] != 12 || got[1] != 5 {
		t.Fatalf("reported pieces = %v, want [12 5]", got)
	}

	// No sink, no request, no response: nothing happens and nothing panics.
	ReportStreamProgress(context.Background(), 9)
	ReportResponseProgress(nil, 9)
	ReportResponseProgress(&http.Response{}, 9)
	if base := context.Background(); WithStreamProgress(base, nil) != base {
		t.Fatal("a nil sink must leave the context alone")
	}

	// A restart reaches the sink as its own value, through either entry.
	got = nil
	RestartStreamProgress(ctx)
	RestartResponseProgress(&http.Response{Request: req})
	RestartStreamProgress(context.Background())
	RestartResponseProgress(nil)
	RestartResponseProgress(&http.Response{})
	if len(got) != 2 || got[0] != StreamProgressRestart || got[1] != StreamProgressRestart {
		t.Fatalf("restarts = %v", got)
	}

	// An auxiliary request opts out: nothing it streams or restarts is seen.
	got = nil
	quiet := WithoutStreamProgress(ctx)
	quietReq, err := http.NewRequestWithContext(quiet, http.MethodPost, "http://example.invalid", nil)
	if err != nil {
		t.Fatal(err)
	}
	ReportStreamProgress(quiet, 7)
	RestartStreamProgress(quiet)
	ReportResponseProgress(&http.Response{Request: quietReq}, 7)
	RestartResponseProgress(&http.Response{Request: quietReq})
	if len(got) != 0 {
		t.Fatalf("an opted-out context reached the sink: %v", got)
	}
}

// Every client that streams through the chunk channel reports its text
// through the one place that drains it.
func TestWatchStreamReportsStreamedText(t *testing.T) {
	total := 0
	ctx := WithStreamProgress(context.Background(), func(chars int) { total += chars })
	chunks := make(chan StreamChunk, 4)
	chunks <- StreamChunk{Text: "hello "}
	chunks <- StreamChunk{}
	chunks <- StreamChunk{Text: "world"}
	chunks <- StreamChunk{Done: true}
	close(chunks)
	res := WatchStream(ctx, chunks, DefaultWatchdogConfig(), nil)
	if res.Text != "hello world" || total != len("hello world") {
		t.Fatalf("text %q, reported %d chars", res.Text, total)
	}
}
