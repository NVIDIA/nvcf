/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"nvcf-grpc-proxy/proxy/metrics"
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/quic-go/quic-go"
	"golang.org/x/net/http2"
)

func TestClassifyCloseError(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantCode   string
		wantDetail string // substring, empty means do not check
		wantRemote *bool
	}{
		{name: "nil is none", err: nil, wantCode: CloseCodeNone},
		{
			name:       "quic application error keeps code and reason",
			err:        &quic.ApplicationError{ErrorCode: 0x101, ErrorMessage: "worker shutting down", Remote: true},
			wantCode:   CloseCodeQUICApplication,
			wantDetail: `code=257 reason="worker shutting down"`,
			wantRemote: boolPtr(true),
		},
		{
			name:       "quic application error records local close",
			err:        &quic.ApplicationError{ErrorCode: 0x0, ErrorMessage: "", Remote: false},
			wantCode:   CloseCodeQUICApplication,
			wantRemote: boolPtr(false),
		},
		{
			name:       "quic transport error",
			err:        &quic.TransportError{ErrorCode: quic.ConnectionRefused, ErrorMessage: "refused", Remote: true},
			wantCode:   CloseCodeQUICTransport,
			wantDetail: "refused",
			wantRemote: boolPtr(true),
		},
		{
			name:       "quic stream reset",
			err:        &quic.StreamError{StreamID: 7, ErrorCode: 42, Remote: true},
			wantCode:   CloseCodeQUICStreamReset,
			wantDetail: "stream=7 code=42",
			wantRemote: boolPtr(true),
		},
		{name: "quic idle timeout", err: &quic.IdleTimeoutError{}, wantCode: CloseCodeQUICIdleTimeout},
		{name: "quic handshake timeout", err: &quic.HandshakeTimeoutError{}, wantCode: CloseCodeQUICHandshakeTimeout},
		{
			name:       "quic stateless reset is always remote",
			err:        &quic.StatelessResetError{},
			wantCode:   CloseCodeQUICStatelessReset,
			wantRemote: boolPtr(true),
		},
		{
			name:       "http2 goaway keeps error code and debug data",
			err:        http2.GoAwayError{LastStreamID: 9, ErrCode: http2.ErrCodeNo, DebugData: "graceful"},
			wantCode:   CloseCodeH2GoAway,
			wantDetail: `debug="graceful"`,
			wantRemote: boolPtr(true),
		},
		{
			name:       "http2 stream error",
			err:        http2.StreamError{StreamID: 3, Code: http2.ErrCodeCancel},
			wantCode:   CloseCodeH2Stream,
			wantDetail: "stream=3",
		},
		{name: "http2 connection error", err: http2.ConnectionError(http2.ErrCodeProtocol), wantCode: CloseCodeH2Connection},
		{name: "eof is a clean peer close", err: io.EOF, wantCode: CloseCodeEOF, wantRemote: boolPtr(true)},
		{name: "unexpected eof", err: io.ErrUnexpectedEOF, wantCode: CloseCodeEOF, wantRemote: boolPtr(true)},
		{name: "econnreset", err: syscall.ECONNRESET, wantCode: CloseCodeReset, wantRemote: boolPtr(true)},
		{name: "epipe", err: syscall.EPIPE, wantCode: CloseCodeReset, wantRemote: boolPtr(true)},
		{name: "closed conn", err: net.ErrClosed, wantCode: CloseCodeClosedConn},
		{name: "context canceled", err: context.Canceled, wantCode: CloseCodeContextCanceled},
		{name: "deadline exceeded", err: context.DeadlineExceeded, wantCode: CloseCodeContextCanceled},
		{name: "net timeout", err: os.ErrDeadlineExceeded, wantCode: CloseCodeTimeout},
		{name: "unrecognised", err: errors.New("something else"), wantCode: CloseCodeUnknown, wantDetail: "something else"},
		{
			name:       "wrapped errors are still classified",
			err:        fmt.Errorf("reading from worker: %w", io.EOF),
			wantCode:   CloseCodeEOF,
			wantRemote: boolPtr(true),
		},
		{
			name:       "wrapped quic error keeps its code rather than degrading to timeout",
			err:        fmt.Errorf("stream failed: %w", &quic.ApplicationError{ErrorCode: 5, ErrorMessage: "bye"}),
			wantCode:   CloseCodeQUICApplication,
			wantDetail: `code=5 reason="bye"`,
			wantRemote: boolPtr(false),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyCloseError(tc.err)
			if got.Code != tc.wantCode {
				t.Errorf("code = %q, want %q (detail %q)", got.Code, tc.wantCode, got.Detail)
			}
			if tc.wantDetail != "" && !strings.Contains(got.Detail, tc.wantDetail) {
				t.Errorf("detail = %q, want it to contain %q", got.Detail, tc.wantDetail)
			}
			switch {
			case tc.wantRemote == nil && got.Remote != nil:
				t.Errorf("Remote = %v, want nil (unknown)", *got.Remote)
			case tc.wantRemote != nil && got.Remote == nil:
				t.Errorf("Remote = nil, want %v", *tc.wantRemote)
			case tc.wantRemote != nil && *got.Remote != *tc.wantRemote:
				t.Errorf("Remote = %v, want %v", *got.Remote, *tc.wantRemote)
			}
		})
	}
}

// Every code the classifier can emit must be in metrics.CloseCodes, or the
// metric gains an un-preinitialised series and absent() alerts misfire.
func TestEveryEmittedCodeIsPreInitialised(t *testing.T) {
	emitted := []error{
		nil,
		&quic.ApplicationError{}, &quic.TransportError{}, &quic.StreamError{},
		&quic.IdleTimeoutError{}, &quic.HandshakeTimeoutError{}, &quic.StatelessResetError{},
		http2.GoAwayError{}, http2.StreamError{}, http2.ConnectionError(http2.ErrCodeProtocol),
		io.EOF, syscall.ECONNRESET, net.ErrClosed, context.Canceled,
		os.ErrDeadlineExceeded, errors.New("x"),
	}
	seen := map[string]bool{}
	for _, err := range emitted {
		code := ClassifyCloseError(err).Code
		seen[code] = true
		if !slices.Contains(metrics.CloseCodes, code) {
			t.Errorf("classifier emits %q which is missing from metrics.CloseCodes", code)
		}
	}
	// And the reverse: no stale entries that can never be produced.
	for _, code := range metrics.CloseCodes {
		if !seen[code] {
			t.Errorf("metrics.CloseCodes lists %q but no input produces it", code)
		}
	}
}

func TestCloseFuncConnRecordsFirstError(t *testing.T) {
	first := errors.New("first")
	second := errors.New("second")
	fc := &CloseFuncConn{Conn: &stubConn{readErrs: []error{first, second}}, onClose: func() {}}

	buf := make([]byte, 1)
	_, _ = fc.Read(buf)
	_, _ = fc.Read(buf)

	if got := fc.FirstError(); !errors.Is(got, first) {
		t.Errorf("FirstError() = %v, want %v: the original fault must survive the cascade", got, first)
	}
}

func TestCloseFuncConnNoErrorWhenClean(t *testing.T) {
	fc := &CloseFuncConn{Conn: &stubConn{}, onClose: func() {}}
	if _, err := fc.Write([]byte("x")); err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}
	if got := fc.FirstError(); got != nil {
		t.Errorf("FirstError() = %v, want nil", got)
	}
	if code := ClassifyCloseError(fc.FirstError()).Code; code != CloseCodeNone {
		t.Errorf("code = %q, want %q", code, CloseCodeNone)
	}
}

func boolPtr(b bool) *bool { return &b }

type stubConn struct {
	net.Conn
	readErrs  []error
	writeErrs []error
	n         int
	w         int
}

func (s *stubConn) Read([]byte) (int, error) {
	if s.n < len(s.readErrs) {
		err := s.readErrs[s.n]
		s.n++
		return 0, err
	}
	return 0, nil
}

func (s *stubConn) Write(b []byte) (int, error) {
	if s.w < len(s.writeErrs) {
		err := s.writeErrs[s.w]
		s.w++
		return 0, err
	}
	return len(b), nil
}
func (s *stubConn) Close() error { return nil }

func TestMarkClosedFirstWriterWins(t *testing.T) {
	w := NewWorkerConnection(uuid.New(), "fn", "ver", func() {}, func() {})
	if !w.ClosedAt().IsZero() {
		t.Fatal("ClosedAt() should be zero before any close is stamped")
	}
	first := time.Now()
	second := first.Add(time.Second)
	w.MarkClosed(first)
	w.MarkClosed(second)
	if got := w.ClosedAt(); !got.Equal(first) {
		t.Errorf("ClosedAt() = %v, want %v: a later cascade must not overwrite the real close", got, first)
	}
}

func TestSetCloseErrorFirstWriterWinsAndIgnoresNil(t *testing.T) {
	w := NewWorkerConnection(uuid.New(), "fn", "ver", func() {}, func() {})
	if w.CloseError() != nil {
		t.Fatal("CloseError() should be nil before any error is recorded")
	}
	// A nil must not occupy the slot, or the real error that follows is lost.
	w.SetCloseError(nil)
	real := errors.New("real cause")
	w.SetCloseError(real)
	w.SetCloseError(errors.New("cascade"))
	if got := w.CloseError(); !errors.Is(got, real) {
		t.Errorf("CloseError() = %v, want %v", got, real)
	}
}

func TestCloseFuncConnRecordsFirstWriteError(t *testing.T) {
	first := errors.New("first write failure")
	second := errors.New("second write failure")
	fc := &CloseFuncConn{Conn: &stubConn{writeErrs: []error{first, second}}, onClose: func() {}}

	_, _ = fc.Write([]byte("a"))
	_, _ = fc.Write([]byte("b"))

	if got := fc.FirstError(); !errors.Is(got, first) {
		t.Errorf("FirstError() = %v, want %v: the first write failure must survive", got, first)
	}
}

func TestCloseFuncConnFirstErrorSpansReadAndWrite(t *testing.T) {
	writeErr := errors.New("write failed first")
	readErr := errors.New("read failed after")
	fc := &CloseFuncConn{
		Conn:    &stubConn{writeErrs: []error{writeErr}, readErrs: []error{readErr}},
		onClose: func() {},
	}

	_, _ = fc.Write([]byte("a"))
	_, _ = fc.Read(make([]byte, 1))

	if got := fc.FirstError(); !errors.Is(got, writeErr) {
		t.Errorf("FirstError() = %v, want %v: first-writer-wins must hold across both directions", got, writeErr)
	}
}

// Peer-supplied reason text reaches logs and spans, so it must be bounded and
// stripped of anything that could forge a log line.
func TestSanitizePeerText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty stays empty", in: "", want: ""},
		{name: "ordinary reason is preserved", in: "worker shutting down", want: "worker shutting down"},
		{name: "newlines are stripped", in: "line one\nline two", want: "line oneline two"},
		{name: "carriage returns are stripped", in: "a\r\nb", want: "ab"},
		{name: "control characters are stripped", in: "a\x00\x07b", want: "ab"},
		{name: "tabs are stripped", in: "a\tb", want: "ab"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizePeerText(tc.in); got != tc.want {
				t.Errorf("sanitizePeerText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSanitizePeerTextTruncates(t *testing.T) {
	got := sanitizePeerText(strings.Repeat("x", maxDetailLen*4))
	if !strings.HasSuffix(got, "[truncated]") {
		t.Errorf("long input was not marked truncated: %q", got)
	}
	if len(got) > maxDetailLen {
		t.Errorf("sanitized output is %d bytes, which exceeds the advertised bound of %d", len(got), maxDetailLen)
	}
}

// Truncating at a byte offset can split a multi-byte rune. The result must
// still be valid UTF-8, or it corrupts whatever consumes the log.
func TestSanitizePeerTextTruncationKeepsValidUTF8(t *testing.T) {
	// Three-byte runes do not divide evenly into maxDetailLen, so this
	// guarantees the cut lands mid-rune.
	got := sanitizePeerText(strings.Repeat("世", maxDetailLen))
	if !utf8.ValidString(got) {
		t.Errorf("sanitized output is not valid UTF-8: %q", got)
	}
}

// A peer must not be able to smuggle a fake field into the classified detail.
func TestClassifyCloseErrorSanitizesPeerReason(t *testing.T) {
	hostile := &quic.ApplicationError{
		ErrorCode:    7,
		ErrorMessage: "ok\n\tlevel=fatal msg=\"forged\"",
	}
	detail := ClassifyCloseError(hostile).Detail
	if strings.ContainsAny(detail, "\n\r\t") {
		t.Errorf("detail carries control characters from the peer: %q", detail)
	}
	if !strings.Contains(detail, "code=7") {
		t.Errorf("detail lost the protocol code, which is the bounded part: %q", detail)
	}
}

// The whole point of closedAt is to exclude teardown latency. If eviction runs
// long after the session ended, held_for must still reflect the session, not
// the session plus our cleanup.
func TestClosedAtExcludesTeardownLatency(t *testing.T) {
	w := NewWorkerConnection(uuid.New(), "fn", "ver", func() {}, func() {})
	sessionEnd := w.CreatedAt.Add(15 * time.Second)

	// Whoever noticed first stamps it.
	w.MarkClosed(sessionEnd)
	// Teardown then cascades, arbitrarily later.
	w.MarkClosed(sessionEnd.Add(30 * time.Second))
	w.MarkClosed(sessionEnd.Add(90 * time.Second))

	heldFor := w.ClosedAt().Sub(w.CreatedAt)
	if heldFor != 15*time.Second {
		t.Errorf("held_for = %v, want 15s: teardown latency must not inflate the tunnel lifetime", heldFor)
	}
}

func TestDetailOmitsEmptyPeerFields(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantContain string
		wantAbsent  string
	}{
		{
			name:        "quic application error without a reason omits the field",
			err:         &quic.ApplicationError{ErrorCode: 3},
			wantContain: "code=3",
			wantAbsent:  "reason=",
		},
		{
			name:        "quic application error with a reason includes it",
			err:         &quic.ApplicationError{ErrorCode: 3, ErrorMessage: "draining"},
			wantContain: `reason="draining"`,
		},
		{
			name:        "quic transport error without a reason omits the field",
			err:         &quic.TransportError{ErrorCode: quic.NoError},
			wantContain: "code=",
			wantAbsent:  "reason=",
		},
		{
			name:        "goaway without debug data omits the field",
			err:         http2.GoAwayError{LastStreamID: 4, ErrCode: http2.ErrCodeNo},
			wantContain: "last_stream=4",
			wantAbsent:  "debug=",
		},
		{
			name:        "goaway with debug data includes it",
			err:         http2.GoAwayError{LastStreamID: 4, ErrCode: http2.ErrCodeNo, DebugData: "bye"},
			wantContain: `debug="bye"`,
		},
		{
			name:        "a reason of only control characters is dropped, not emitted empty",
			err:         &quic.ApplicationError{ErrorCode: 9, ErrorMessage: "\n\r\x00"},
			wantContain: "code=9",
			wantAbsent:  "reason=",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyCloseError(tc.err).Detail
			if !strings.Contains(got, tc.wantContain) {
				t.Errorf("detail = %q, want it to contain %q", got, tc.wantContain)
			}
			if tc.wantAbsent != "" && strings.Contains(got, tc.wantAbsent) {
				t.Errorf("detail = %q, want it to omit %q", got, tc.wantAbsent)
			}
		})
	}
}
