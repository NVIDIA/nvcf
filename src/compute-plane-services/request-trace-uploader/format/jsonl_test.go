// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package format_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/format"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/transform"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/transform/suppress"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/uploadertest"
)

func newJSONL(t *testing.T, target format.Target) format.Writer {
	t.Helper()
	w, err := format.GzipJSONL{}.NewWriter(target)
	if err != nil {
		t.Fatalf("NewWriter() error = %v", err)
	}
	return w
}

func write(t *testing.T, w format.Writer, payloads ...string) {
	t.Helper()
	for i, payload := range payloads {
		if err := w.Write(transform.Record{Line: i + 1, Payload: []byte(payload)}); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(data, []byte("\n")) {
		t.Fatalf("transport does not end with a newline: %q", data)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestGzipJSONLReportsItsFormat(t *testing.T) {
	if got := (format.GzipJSONL{}).Format(); got != backend.FormatGzipJSONL {
		t.Fatalf("Format() = %q, want %q", got, backend.FormatGzipJSONL)
	}
}

func TestGzipJSONLCloseReportsPublishedFile(t *testing.T) {
	target := newTarget(t, "bucket")
	w := newJSONL(t, target)
	write(t, w, `{"a":1}`, `{"b":2}`)
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	outputs := w.Outputs()
	if len(outputs) != 1 {
		t.Fatalf("Outputs() = %+v, want one file", outputs)
	}
	out := outputs[0]
	if want := target.Path("bucket", ".jsonl.gz"); out.Path != want {
		t.Fatalf("Path = %q, want %q", out.Path, want)
	}
	data, err := os.ReadFile(out.Path)
	if err != nil {
		t.Fatalf("published file: %v", err)
	}
	sum := sha256.Sum256(data)
	if out.Destination != "bucket" || out.Rows != 2 || out.Bytes != int64(len(data)) || out.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("Output = %+v, want destination bucket, 2 rows, %d bytes, sha256 of the file", out, len(data))
	}
	if got := readLines(t, out.Path); strings.Join(got, "|") != `{"a":1}|{"b":2}` {
		t.Fatalf("lines = %q", got)
	}
}

func TestGzipJSONLUnfinishedFilesMatchRecoveryGlob(t *testing.T) {
	target := newTarget(t)
	w := newJSONL(t, target)
	write(t, w, `{"a":1}`)

	names := dirNames(t, target.Dir)
	if len(names) == 0 {
		t.Fatal("no unfinished file while writing")
	}
	for _, name := range names {
		if ok, _ := filepath.Match(target.SegmentID+".*", name); !ok {
			t.Fatalf("unfinished file %q is not named after the segment id, so recovery would not remove it", name)
		}
	}
	if _, err := os.Stat(target.Path("", ".jsonl.gz")); !os.IsNotExist(err) {
		t.Fatalf("final file exists before Close: %v", err)
	}
	if err := w.Abort(); err != nil {
		t.Fatalf("Abort() error = %v", err)
	}
}

func TestGzipJSONLAbortLeavesNothing(t *testing.T) {
	target := newTarget(t)
	w := newJSONL(t, target)
	write(t, w, `{"a":1}`)
	if err := w.Abort(); err != nil {
		t.Fatalf("Abort() error = %v", err)
	}
	if err := w.Abort(); err != nil {
		t.Fatalf("second Abort() error = %v", err)
	}
	if names := dirNames(t, target.Dir); len(names) != 0 {
		t.Fatalf("files left after Abort: %q", names)
	}
	if outputs := w.Outputs(); len(outputs) != 0 {
		t.Fatalf("Outputs() = %+v after Abort", outputs)
	}
}

func TestGzipJSONLCloseWithNoRecordsPublishesNothing(t *testing.T) {
	target := newTarget(t)
	w := newJSONL(t, target)
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if names := dirNames(t, target.Dir); len(names) != 0 {
		t.Fatalf("files left after an empty Close: %q", names)
	}
	if outputs := w.Outputs(); len(outputs) != 0 {
		t.Fatalf("Outputs() = %+v, an empty file is not an upload", outputs)
	}
}

func TestGzipJSONLWriteAfterCloseFails(t *testing.T) {
	w := newJSONL(t, newTarget(t))
	write(t, w, `{"a":1}`)
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := w.Write(transform.Record{Payload: []byte(`{"b":2}`)}); err == nil {
		t.Fatal("Write() after Close error = nil")
	}
}

// The caller owns the segment id. A writer only needs a plain file name to
// start its files with, not a journal key.
func TestGzipJSONLNamesFilesAfterAnyPlainSegmentID(t *testing.T) {
	target := newTarget(t)
	target.SegmentID = "segment-7"
	w := newJSONL(t, target)
	write(t, w, `{"a":1}`)
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	outputs := w.Outputs()
	if want := filepath.Join(target.Dir, "segment-7.e3b0c44298fc1c14.jsonl.gz"); len(outputs) != 1 || outputs[0].Path != want {
		t.Fatalf("Outputs() = %+v, want one file at %q", outputs, want)
	}
}

func TestNewWriterRejectsBadTarget(t *testing.T) {
	control, err := format.GzipJSONL{}.NewWriter(newTarget(t))
	if err != nil {
		t.Fatalf("NewWriter() error = %v for a valid target", err)
	}
	_ = control.Abort()

	tests := []struct {
		name   string
		change func(*format.Target)
	}{
		{"no segment id", func(tg *format.Target) { tg.SegmentID = "" }},
		{"segment id is a path", func(tg *format.Target) { tg.SegmentID = "../" + tg.SegmentID[3:] }},
		{"segment id has a backslash", func(tg *format.Target) { tg.SegmentID = `a\b` }},
		{"segment id is the parent directory", func(tg *format.Target) { tg.SegmentID = ".." }},
		{"relative directory", func(tg *format.Target) { tg.Dir = "transport" }},
		{"missing directory", func(tg *format.Target) { tg.Dir = filepath.Join(tg.Dir, "missing") }},
		{"no destination", func(tg *format.Target) { tg.Destinations = nil }},
		{"two destinations", func(tg *format.Target) { tg.Destinations = []backend.Destination{"a", "b"} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := newTarget(t)
			tt.change(&target)
			w, err := format.GzipJSONL{}.NewWriter(target)
			if err == nil {
				_ = w.Abort()
				t.Fatal("NewWriter() error = nil")
			}
		})
	}
}

var pngURL = "data:image/png;base64," + base64.StdEncoding.EncodeToString(append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{7, 99, 201}, 60)...))

func mediaLine(nca string) string {
	return `{"timestamp":5,"event":{"event_type":"request_payload","event_time_unix_ms":2,"payload":{"request_id":"m","http_request_headers":{"nvcf-ncaid":"` + nca +
		`"},"request":{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"` + pngURL + `"}}]}]}}}}`
}

// The object store keeps records as Dynamo wrote them. Only suppression and
// redaction may change a record; spacing, key order, escapes and number
// spelling of every other record survive.
func TestGzipJSONLKeepsRecordBytesExceptSuppressedAndRedacted(t *testing.T) {
	plain := `{"schema":"dynamo.request.trace.v1", "event_type":"request_end","event_time_unix_ms":1,"request":{"request_id":"a"}}`
	escaped := `{"z":1.50,"event_type":"request_end","event_time_unix_ms":2,"request":{"request_id":"\u00e9\ud83d\ude00 x"},"a":null}`
	redacted := mediaLine("someone")
	suppressed := mediaLine("acme")
	path := filepath.Join(t.TempDir(), "request-trace.000001.jsonl.gz")
	data := uploadertest.NewSegment().Member(plain, escaped).Member(redacted, suppressed).Bytes()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := transform.DefaultConfig()
	cfg.Suppression = suppress.FromIDs([]string{"acme"})
	preparer, err := transform.New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	target := newTarget(t)
	w := newJSONL(t, target)
	result, err := preparer.Prepare(context.Background(), path, w, nil)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if result.Suppressed != 1 || result.RedactedParts != 1 {
		t.Fatalf("result = %+v, want one suppressed and one redacted record", result)
	}

	outputs := w.Outputs()
	if len(outputs) != 1 || outputs[0].Rows != int64(result.Written) {
		t.Fatalf("Outputs() = %+v, want one file with %d rows", outputs, result.Written)
	}
	lines := readLines(t, outputs[0].Path)
	if len(lines) != 4 {
		t.Fatalf("lines = %d, want 4", len(lines))
	}
	if lines[0] != plain || lines[1] != escaped {
		t.Fatalf("unchanged records lost their source bytes:\n%s\n%s", lines[0], lines[1])
	}
	if lines[2] == redacted || !strings.Contains(lines[2], `"redacted":"multimodal"`) {
		t.Fatalf("redacted record = %.120s, want the media replaced", lines[2])
	}
	if !strings.Contains(lines[3], `"payload_suppressed":true`) || strings.Contains(lines[3], "messages") {
		t.Fatalf("suppressed record = %.120s, want the stub", lines[3])
	}
}
