/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package provision

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
)

// Every platform with a prebuilt engine has a download size to announce.
func TestSherpaAssetBytesCoversEveryAsset(t *testing.T) {
	for _, p := range [][2]string{{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "amd64"}, {"darwin", "arm64"}, {"windows", "amd64"}} {
		if _, ok := SherpaAsset(p[0], p[1]); !ok || SherpaAssetBytes(p[0], p[1]) < 10<<20 {
			t.Errorf("%s/%s: engine size %d", p[0], p[1], SherpaAssetBytes(p[0], p[1]))
		}
	}
	if SherpaAssetBytes("freebsd", "amd64") != 0 {
		t.Error("an unsupported platform must report no download")
	}
}

// A progress hook sees the archive bytes arrive, then the extract step
// (here the payload is not a real bzip2 stream, so extraction then fails,
// which is the point: the hook reports before the step runs).
func TestProvisionArchiveReportsProgress(t *testing.T) {
	payload := bytes.Repeat([]byte("B"), 100_000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	var steps []Progress
	ctx := WithProgress(context.Background(), func(p Progress) { steps = append(steps, p) })
	target := filepath.Join(t.TempDir(), "piece")
	if err := ProvisionArchive(ctx, srv.URL+"/assets/piece.tar.bz2", target, 1); err == nil {
		t.Fatal("a non-bzip2 payload must fail extraction")
	}
	if len(steps) < 3 {
		t.Fatalf("steps = %+v", steps)
	}
	first, last := steps[0], steps[len(steps)-1]
	if first.Phase != PhaseDownload || first.Done != 0 || first.File != "piece.tar.bz2" || first.Total != int64(len(payload)) {
		t.Fatalf("first step = %+v", first)
	}
	if last.Phase != PhaseExtract || last.File != "piece.tar.bz2" {
		t.Fatalf("last step = %+v", last)
	}
	if got := steps[len(steps)-2]; got.Phase != PhaseDownload || got.Done != int64(len(payload)) {
		t.Fatalf("download finished at %+v, want %d bytes", got, len(payload))
	}
}

// Without a hook the download path is untouched and a nil hook leaves the
// context as it was.
func TestWithProgressNilHookAndPlainDownload(t *testing.T) {
	ctx := context.Background()
	if WithProgress(ctx, nil) != ctx {
		t.Fatal("a nil hook must leave the context untouched")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("C"), 64))
	}))
	defer srv.Close()
	if err := DownloadArchive(ctx, srv.URL+"/x", filepath.Join(t.TempDir(), "x"), 1); err != nil {
		t.Fatal(err)
	}
	if archiveName("no-slash") != "no-slash" {
		t.Fatal("a bare name is its own archive name")
	}
}
