// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

// countingRecorder records every Write and Flush call, to prove a stream was split.
type countingRecorder struct {
	*httptest.ResponseRecorder
	writes  [][]byte
	flushes int
}

func (c *countingRecorder) Flush() {
	c.flushes++
	c.ResponseRecorder.Flush()
}

func (c *countingRecorder) Write(p []byte) (int, error) {
	c.writes = append(c.writes, append([]byte(nil), p...))
	return c.ResponseRecorder.Write(p)
}

func TestMessagesStreamDefaultKeepsLF(t *testing.T) {
	recorder := postJSONWithHeaders(t, "/v1/messages", messagesBody+`,"stream":true}`, map[string]string{headerOutputChunks: "2"})
	body := recorder.Body.String()
	if strings.Contains(body, "\r") {
		t.Fatalf("default stream contains CR: %q", body)
	}
	if !strings.HasPrefix(body, "event: message_start\ndata: ") {
		t.Fatalf("stream = %q", body)
	}
}

var messageID = regexp.MustCompile(`msg_\d+`)

func TestMessagesStreamLineEndings(t *testing.T) {
	// Message ids advance with every request; the two streams are compared without them.
	lf := messageID.ReplaceAllString(postJSONWithHeaders(t, "/v1/messages", messagesBody+`,"stream":true}`, map[string]string{headerOutputChunks: "2", headerChunk: "ab"}).Body.String(), "msg_N")
	for name, ending := range map[string]string{sseLineEndingCRLF: "\r\n", sseLineEndingCR: "\r"} {
		got := messageID.ReplaceAllString(postJSONWithHeaders(t, "/v1/messages", messagesBody+`,"stream":true}`, map[string]string{
			headerOutputChunks: "2", headerChunk: "ab", headerSSELineEnding: name,
		}).Body.String(), "msg_N")
		if want := strings.ReplaceAll(lf, "\n", ending); got != want {
			t.Fatalf("%s stream differs from the LF stream with its line endings replaced:\n got %q\nwant %q", name, got, want)
		}
		if name == sseLineEndingCR && strings.Contains(got, "\n") {
			t.Fatalf("cr stream still contains LF: %q", got)
		}
	}
	// The same control in the request body (a request with any x-load-tester header ignores the body controls).
	got := postJSONWithHeaders(t, "/v1/messages", messagesBody+`,"stream":true,"`+bodySSELineEnding+`":"crlf"}`, nil).Body.String()
	if !strings.Contains(got, "\r\n\r\n") {
		t.Fatalf("body control did not apply: %q", got)
	}
}

// helloChinese is two three-byte UTF-8 characters, written as escapes to keep the source ASCII.
const helloChinese = "\u4f60\u597d"

func TestMessagesStreamSplitsMultibyteText(t *testing.T) {
	recorder := &countingRecorder{ResponseRecorder: httptest.NewRecorder()}
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(messagesBody+`,"stream":true}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(headerChunk, helloChinese)
	request.Header.Set(headerOutputChunks, "1")
	request.Header.Set(headerSSESplitBytes, "1")
	newRouter().ServeHTTP(recorder, request)

	split := false
	for _, write := range recorder.writes {
		if len(write) != 1 {
			t.Fatalf("write of %d bytes, want 1 byte pieces", len(write))
		}
		if !utf8.Valid(write) {
			split = true
		}
	}
	if !split {
		t.Fatal("no write ended inside a multi-byte character")
	}
	if recorder.flushes < len(recorder.writes) {
		t.Fatalf("%d flushes for %d writes, want a flush after each piece", recorder.flushes, len(recorder.writes))
	}
	if got := recorder.Body.String(); !utf8.ValidString(got) || !strings.Contains(got, `"text":"`+helloChinese+`"`) {
		t.Fatalf("reassembled stream is wrong: %q", got)
	}
}

func TestMessagesStreamRejectsInvalidSSEControls(t *testing.T) {
	for name, headers := range map[string]map[string]string{
		"line ending": {headerSSELineEnding: "tab"},
		"split bytes": {headerSSESplitBytes: "-1"},
	} {
		recorder := postJSONWithHeaders(t, "/v1/messages", messagesBody+`,"stream":true}`, headers)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400: %s", name, recorder.Code, recorder.Body.String())
		}
		assertMessagesError(t, recorder, "invalid_request_error")
	}
}

func TestSSEFormatWriterDefaultReturnsOriginalWriter(t *testing.T) {
	recorder := httptest.NewRecorder()
	if got := newSSEFormatWriter(recorder, "", 0); got != http.ResponseWriter(recorder) {
		t.Fatal("writer was wrapped although no SSE control is set")
	}
	if got := newSSEFormatWriter(recorder, sseLineEndingLF, 0); got != http.ResponseWriter(recorder) {
		t.Fatal("lf must keep the original writer")
	}
}

func TestSSEFormatWriterKeepsEscapedNewlinesInText(t *testing.T) {
	recorder := httptest.NewRecorder()
	w := newSSEFormatWriter(recorder, sseLineEndingCRLF, 0)
	frame := []byte("data: {\"text\":\"a\\nb\"}\n\n")
	if n, err := w.Write(frame); err != nil || n != len(frame) {
		t.Fatalf("Write = %d, %v; want %d, nil", n, err, len(frame))
	}
	want := "data: {\"text\":\"a\\nb\"}\r\n\r\n"
	if !bytes.Equal(recorder.Body.Bytes(), []byte(want)) {
		t.Fatalf("body = %q, want %q", recorder.Body.String(), want)
	}
}
