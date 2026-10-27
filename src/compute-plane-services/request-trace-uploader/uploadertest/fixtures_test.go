// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package uploadertest_test

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/record"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/uploadertest"
)

const eventTime = 1767225600000

// The expected lines follow the field names and declaration order of the
// Dynamo v1.4.0 serde types in lib/llm/src/request_trace/types.rs, and the
// envelope of GzipEntry in lib/llm/src/telemetry/jsonl_gz.rs.
func TestLineBuildersMatchDynamoShape(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{
			name: "request_end",
			got:  uploadertest.RequestEnd("req-1", eventTime),
			want: `{"schema":"dynamo.request.trace.v1","event_type":"request_end","event_time_unix_ms":1767225600000,"event_source":"dynamo","request":{"request_id":"req-1","model":"test-model","input_tokens":12,"output_tokens":34,"total_time_ms":25.5}}`,
		},
		{
			name: "request_payload with headers",
			got:  uploadertest.RequestPayload("req-1", eventTime, map[string]string{"nvcf-ncaid": "nca-test", "accept": "application/json"}),
			want: `{"schema":"dynamo.request.trace.v1","event_type":"request_payload","event_time_unix_ms":1767225600000,"event_source":"dynamo","payload":{"request_id":"req-1","endpoint":"openai.chat_completion","model":"test-model","request":{"model":"test-model","messages":[{"role":"user","content":"synthetic prompt"}]},"response":{"id":"chatcmpl-test","object":"chat.completion","model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"synthetic reply"},"finish_reason":"stop"}]},"http_request_headers":{"accept":"application/json","nvcf-ncaid":"nca-test"},"payload_complete":true}}`,
		},
		{
			name: "request_payload without headers",
			got:  uploadertest.RequestPayload("req-2", eventTime, nil),
			want: `{"schema":"dynamo.request.trace.v1","event_type":"request_payload","event_time_unix_ms":1767225600000,"event_source":"dynamo","payload":{"request_id":"req-2","endpoint":"openai.chat_completion","model":"test-model","request":{"model":"test-model","messages":[{"role":"user","content":"synthetic prompt"}]},"response":{"id":"chatcmpl-test","object":"chat.completion","model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"synthetic reply"},"finish_reason":"stop"}]},"payload_complete":true}}`,
		},
		{
			name: "tool_start",
			got:  uploadertest.Tool(record.EventToolStart, "sess-1", "call-1", eventTime),
			want: `{"schema":"dynamo.request.trace.v1","event_type":"tool_start","event_time_unix_ms":1767225600000,"event_source":"harness","agent_context":{"session_id":"sess-1"},"tool":{"tool_call_id":"call-1","tool_class":"search","started_at_unix_ms":1767225600000,"status":"running"}}`,
		},
		{
			name: "tool_end",
			got:  uploadertest.Tool(record.EventToolEnd, "sess-1", "call-1", eventTime),
			want: `{"schema":"dynamo.request.trace.v1","event_type":"tool_end","event_time_unix_ms":1767225600000,"event_source":"harness","agent_context":{"session_id":"sess-1"},"tool":{"tool_call_id":"call-1","tool_class":"search","status":"succeeded","duration_ms":12.5}}`,
		},
		{
			name: "tool_error",
			got:  uploadertest.Tool(record.EventToolError, "sess-1", "call-2", eventTime),
			want: `{"schema":"dynamo.request.trace.v1","event_type":"tool_error","event_time_unix_ms":1767225600000,"event_source":"harness","agent_context":{"session_id":"sess-1"},"tool":{"tool_call_id":"call-2","tool_class":"search","status":"error","error_type":"timeout"}}`,
		},
		{
			name: "wrapped in the file sink envelope",
			got:  uploadertest.Wrap(`{"event_type":"request_end"}`, 42),
			want: `{"timestamp":42,"event":{"event_type":"request_end"}}`,
		},
		{
			name: "Dynamo v1.3 AuditRecord",
			got:  uploadertest.LegacyAuditRecord("req-1"),
			want: `{"schema_version":1,"request_id":"req-1","requested_streaming":false,"model":"test-model"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("line mismatch\n got: %s\nwant: %s", tc.got, tc.want)
			}
		})
	}
}

func TestLegacyAuditRecordIsValidJSONForAnyID(t *testing.T) {
	id := "req-\x01-\"quoted\""
	line := uploadertest.LegacyAuditRecord(id)

	var got struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("line %s is not valid JSON: %v", line, err)
	}
	if got.RequestID != id {
		t.Errorf("request_id = %q, want %q", got.RequestID, id)
	}
}

// gzipMembers splits data into its gzip members and returns each member's
// lines, using only the standard library as the oracle.
func gzipMembers(t *testing.T, data []byte) [][]string {
	t.Helper()
	br := bufio.NewReader(bytes.NewReader(data))
	var members [][]string
	for {
		if _, err := br.Peek(1); errors.Is(err, io.EOF) {
			return members
		}
		zr, err := gzip.NewReader(br)
		if err != nil {
			t.Fatalf("member %d header: %v", len(members), err)
		}
		zr.Multistream(false)
		body, err := io.ReadAll(zr)
		if err != nil {
			t.Fatalf("member %d body: %v", len(members), err)
		}
		members = append(members, strings.Split(strings.TrimSuffix(string(body), "\n"), "\n"))
	}
}

func TestSegmentWritesOneGzipMemberPerFlush(t *testing.T) {
	first := uploadertest.Wrap(uploadertest.RequestEnd("req-1", eventTime), 10)
	second := uploadertest.Wrap(uploadertest.RequestPayload("req-1", eventTime, nil), 11)
	bare := uploadertest.RequestEnd("req-2", eventTime)

	data := uploadertest.NewSegment().Member(first, second).Member(bare).Bytes()

	got := gzipMembers(t, data)
	want := [][]string{{first, second}, {bare}}
	if len(got) != len(want) {
		t.Fatalf("members = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if strings.Join(got[i], "\n") != strings.Join(want[i], "\n") {
			t.Errorf("member %d lines = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestPartialMemberShapes(t *testing.T) {
	a := uploadertest.Wrap(uploadertest.RequestEnd("req-a", eventTime), 1)
	b := uploadertest.Wrap(uploadertest.RequestPayload("req-b", eventTime, nil), 2)
	c := uploadertest.Wrap(uploadertest.RequestEnd("req-c", eventTime), 3)

	t.Run("truncated tail keeps the earlier members whole", func(t *testing.T) {
		data := uploadertest.NewSegment().Member(a).PartialMember(b, c).Bytes()
		whole := uploadertest.NewSegment().Member(a).Bytes()
		if !bytes.HasPrefix(data, whole) || len(data) <= len(whole) {
			t.Fatalf("segment does not start with the whole first member followed by more bytes")
		}

		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		body, err := io.ReadAll(zr)
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("read error = %v, want unexpected EOF", err)
		}
		if !strings.HasPrefix(string(body), a+"\n") {
			t.Errorf("decoded prefix lost the first member")
		}
	})

	t.Run("partial member mid-file leaves later members whole", func(t *testing.T) {
		data := uploadertest.NewSegment().Member(a).PartialMember(b).Member(c).Bytes()
		head := uploadertest.NewSegment().Member(a).Bytes()
		tail := uploadertest.NewSegment().Member(c).Bytes()
		full := uploadertest.NewSegment().Member(b).Bytes()

		if !bytes.HasPrefix(data, head) || !bytes.HasSuffix(data, tail) {
			t.Fatalf("segment is not whole member, partial member, whole member")
		}
		middle := data[len(head) : len(data)-len(tail)]
		if len(middle) == 0 || len(middle) >= len(full) || !bytes.HasPrefix(full, middle) {
			t.Errorf("middle is %d bytes, want a strict non-empty prefix of the %d byte member", len(middle), len(full))
		}
	})

	t.Run("zero length", func(t *testing.T) {
		if got := uploadertest.NewSegment().Bytes(); len(got) != 0 {
			t.Errorf("empty segment = %d bytes, want 0", len(got))
		}
	})
}

func TestSpoolRollFollowsDynamoNumbering(t *testing.T) {
	cases := []struct {
		name     string
		existing []int
		foreign  []string
		rolls    int
		want     string
	}{
		{name: "empty spool starts at zero", rolls: 1, want: "request-trace.000000.jsonl.gz"},
		{name: "rolls count up", rolls: 3, want: "request-trace.000002.jsonl.gz"},
		{name: "restart continues past the highest index", existing: []int{3, 7}, rolls: 1, want: "request-trace.000008.jsonl.gz"},
		{name: "index grows past six digits", existing: []int{999999}, rolls: 1, want: "request-trace.1000000.jsonl.gz"},
		{name: "other families do not count", foreign: []string{"other.000005.jsonl.gz", "request-trace.000009.jsonl"}, rolls: 1, want: "request-trace.000000.jsonl.gz"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spool := uploadertest.NewSpool(t, "request-trace")
			if spool.Dir == "" {
				t.Fatal("spool has no directory; refusing to write fixtures into the working directory")
			}
			for _, index := range tc.existing {
				spool.WriteAt(index, nil)
			}
			for _, name := range tc.foreign {
				if err := os.WriteFile(filepath.Join(spool.Dir, name), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			data := uploadertest.NewSegment().Member(uploadertest.RequestEnd("req", eventTime)).Bytes()
			var path string
			for range tc.rolls {
				path = spool.Roll(data)
			}

			if want := filepath.Join(spool.Dir, tc.want); path != want {
				t.Fatalf("last roll = %q, want %q", path, want)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read rolled segment: %v", err)
			}
			if !bytes.Equal(got, data) {
				t.Errorf("rolled segment content differs from the data written")
			}
		})
	}
}
