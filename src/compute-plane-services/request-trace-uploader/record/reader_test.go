// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package record

import (
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSegment(t *testing.T, lines ...string) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	for _, line := range lines {
		if _, err := gz.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("write line: %v", err)
		}
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	path := filepath.Join(t.TempDir(), "request-trace.000000.jsonl.gz")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write segment: %v", err)
	}
	return path
}

func readAll(t *testing.T, path string) ([]*Record, Stats, error) {
	t.Helper()
	reader, err := Open(path)
	if err != nil {
		return nil, Stats{}, err
	}
	defer reader.Close()

	var records []*Record
	for reader.Next() {
		records = append(records, reader.Record())
	}
	return records, reader.Stats(), reader.Err()
}

func TestReadsEveryEventType(t *testing.T) {
	path := writeSegment(t,
		`{"schema":"dynamo.request.trace.v1","event_type":"request_end","event_time_unix_ms":1,"request":{"request_id":"req-1","model":"m"}}`,
		`{"schema":"dynamo.request.trace.v1","event_type":"request_payload","event_time_unix_ms":2,"payload":{"request_id":"req-1","endpoint":"openai.chat_completion","payload_complete":true,"http_request_headers":{"nvcf-ncaid":"acme"}}}`,
		`{"schema":"dynamo.request.trace.v1","event_type":"tool_start","event_time_unix_ms":3,"agent_context":{"session_id":"sess-1"},"tool":{"tool_call_id":"t-1","tool_class":"search"}}`,
		`{"schema":"dynamo.request.trace.v1","event_type":"tool_end","event_time_unix_ms":4,"agent_context":{"session_id":"sess-1"},"tool":{"tool_call_id":"t-1","tool_class":"search"}}`,
		`{"schema":"dynamo.request.trace.v1","event_type":"tool_error","event_time_unix_ms":5,"agent_context":{"session_id":"sess-1"},"tool":{"tool_call_id":"t-2","tool_class":"search","error_type":"timeout"}}`,
	)

	records, stats, err := readAll(t, path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(records) != 5 || stats.Records != 5 {
		t.Fatalf("records = %d, stats = %+v, want 5", len(records), stats)
	}
	for _, want := range []EventType{EventRequestEnd, EventRequestPayload, EventToolStart, EventToolEnd, EventToolError} {
		if stats.ByEventType[want] != 1 {
			t.Errorf("event type %q count = %d, want 1", want, stats.ByEventType[want])
		}
	}
	if stats.Unknown != 0 || stats.Unparseable != 0 {
		t.Errorf("unknown = %d, unparseable = %d, want 0", stats.Unknown, stats.Unparseable)
	}
}

func TestRequestIDResolvesPerRecordType(t *testing.T) {
	path := writeSegment(t,
		`{"schema":"dynamo.request.trace.v1","event_type":"request_end","event_time_unix_ms":1,"request":{"request_id":"from-request"}}`,
		`{"schema":"dynamo.request.trace.v1","event_type":"request_payload","event_time_unix_ms":2,"payload":{"request_id":"from-payload","payload_complete":true}}`,
		`{"schema":"dynamo.request.trace.v1","event_type":"tool_end","event_time_unix_ms":3,"agent_context":{"session_id":"sess-9"},"tool":{"tool_call_id":"t","tool_class":"c"}}`,
	)

	records, _, err := readAll(t, path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if id, ok := records[0].RequestID(); !ok || id != "from-request" {
		t.Errorf("request_end id = %q ok = %v, want from-request true", id, ok)
	}
	if id, ok := records[1].RequestID(); !ok || id != "from-payload" {
		t.Errorf("request_payload id = %q ok = %v, want from-payload true", id, ok)
	}
	if _, ok := records[2].RequestID(); ok {
		t.Error("tool_end reported a request id, want none")
	}
	if id, ok := records[2].SessionID(); !ok || id != "sess-9" {
		t.Errorf("tool_end session = %q ok = %v, want sess-9 true", id, ok)
	}
}

func TestOneBadLineDoesNotDiscardTheSegment(t *testing.T) {
	path := writeSegment(t,
		`{"schema":"dynamo.request.trace.v1","event_type":"request_end","event_time_unix_ms":1,"request":{"request_id":"a"}}`,
		`{"this is not valid json`,
		`{"schema":"dynamo.request.trace.v1","event_type":"request_end","event_time_unix_ms":2,"request":{"request_id":"b"}}`,
	)

	records, stats, err := readAll(t, path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %d, want 2 good records around the bad line", len(records))
	}
	if stats.Unparseable != 1 {
		t.Errorf("unparseable = %d, want 1", stats.Unparseable)
	}
}

func TestUnknownEventTypeIsKeptAndCounted(t *testing.T) {
	path := writeSegment(t,
		`{"schema":"dynamo.request.trace.v1","event_type":"request_squirrel","event_time_unix_ms":1,"squirrel":{"nuts":3}}`,
	)

	records, stats, err := readAll(t, path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(records) != 1 || stats.Unknown != 1 {
		t.Fatalf("records = %d unknown = %d, want 1 and 1", len(records), stats.Unknown)
	}
	if !bytes.Contains(records[0].Raw, []byte("squirrel")) {
		t.Error("unknown record lost its original bytes")
	}
}

func TestLegacyAuditRecordIsRejected(t *testing.T) {
	path := writeSegment(t,
		`{"schema_version":1,"request_id":"req-1","requested_streaming":false,"model":"m"}`,
	)

	_, _, err := readAll(t, path)
	if !errors.Is(err, ErrLegacyAuditRecord) {
		t.Fatalf("err = %v, want ErrLegacyAuditRecord", err)
	}
	if !strings.Contains(err.Error(), "v1.4.0") {
		t.Errorf("err = %v, want it to name the minimum version", err)
	}
}

func TestTimestampWrappedFormParses(t *testing.T) {
	path := writeSegment(t,
		`{"timestamp":1777312801000,"event":{"schema":"dynamo.request.trace.v1","event_type":"request_end","event_time_unix_ms":1,"request":{"request_id":"wrapped"}}}`,
	)

	records, _, err := readAll(t, path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if id, ok := records[0].RequestID(); !ok || id != "wrapped" {
		t.Errorf("id = %q ok = %v, want wrapped true", id, ok)
	}
}

func TestConcatenatedGzipMembersAreRead(t *testing.T) {
	var buf bytes.Buffer
	for _, line := range []string{
		`{"schema":"dynamo.request.trace.v1","event_type":"request_end","event_time_unix_ms":1,"request":{"request_id":"a"}}`,
		`{"schema":"dynamo.request.trace.v1","event_type":"request_end","event_time_unix_ms":2,"request":{"request_id":"b"}}`,
	} {
		gz := gzip.NewWriter(&buf)
		if _, err := gz.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("write gzip member: %v", err)
		}
		if err := gz.Close(); err != nil {
			t.Fatalf("close gzip member: %v", err)
		}
	}
	path := filepath.Join(t.TempDir(), "request-trace.000000.jsonl.gz")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	records, _, err := readAll(t, path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %d, want 2 across concatenated members", len(records))
	}
}

func TestOversizedLineDoesNotStopTheScan(t *testing.T) {
	huge := `{"schema":"dynamo.request.trace.v1","event_type":"request_payload","event_time_unix_ms":1,"payload":{"request_id":"huge","payload_complete":true,"pad":"` +
		strings.Repeat("x", maxLineBytes+1024) + `"}}`

	path := writeSegment(t,
		`{"schema":"dynamo.request.trace.v1","event_type":"request_end","event_time_unix_ms":1,"request":{"request_id":"before"}}`,
		huge,
		`{"schema":"dynamo.request.trace.v1","event_type":"request_end","event_time_unix_ms":2,"request":{"request_id":"after"}}`,
	)

	records, stats, err := readAll(t, path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %d, want the records either side of the oversized line", len(records))
	}
	first, _ := records[0].RequestID()
	second, _ := records[1].RequestID()
	if first != "before" || second != "after" {
		t.Errorf("ids = %q %q, want before after", first, second)
	}
	if stats.Oversized != 1 || stats.Unparseable != 1 {
		t.Errorf("oversized = %d unparseable = %d, want 1 and 1", stats.Oversized, stats.Unparseable)
	}
}

func TestSegmentWithNoTrailingNewlineIsRead(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write([]byte(`{"schema":"dynamo.request.trace.v1","event_type":"request_end","event_time_unix_ms":1,"request":{"request_id":"last"}}`)); err != nil {
		t.Fatalf("write gzip member: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip member: %v", err)
	}
	path := filepath.Join(t.TempDir(), "request-trace.000000.jsonl.gz")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	records, _, err := readAll(t, path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want the final unterminated record", len(records))
	}
}
