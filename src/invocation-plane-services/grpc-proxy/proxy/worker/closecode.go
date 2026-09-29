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
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"github.com/quic-go/quic-go"
	"golang.org/x/net/http2"
)

// Close code constants live in the metrics package alongside the other label
// values. Re-exported here so callers working with connections do not need to
// import metrics for them.
const (
	CloseCodeNone                 = metrics.CloseCodeNone
	CloseCodeEOF                  = metrics.CloseCodeEOF
	CloseCodeReset                = metrics.CloseCodeReset
	CloseCodeTimeout              = metrics.CloseCodeTimeout
	CloseCodeClosedConn           = metrics.CloseCodeClosedConn
	CloseCodeContextCanceled      = metrics.CloseCodeContextCanceled
	CloseCodeQUICIdleTimeout      = metrics.CloseCodeQUICIdleTimeout
	CloseCodeQUICApplication      = metrics.CloseCodeQUICApplication
	CloseCodeQUICTransport        = metrics.CloseCodeQUICTransport
	CloseCodeQUICStreamReset      = metrics.CloseCodeQUICStreamReset
	CloseCodeQUICHandshakeTimeout = metrics.CloseCodeQUICHandshakeTimeout
	CloseCodeQUICStatelessReset   = metrics.CloseCodeQUICStatelessReset
	CloseCodeH2GoAway             = metrics.CloseCodeH2GoAway
	CloseCodeH2Stream             = metrics.CloseCodeH2Stream
	CloseCodeH2Connection         = metrics.CloseCodeH2Connection
	CloseCodeUnknown              = metrics.CloseCodeUnknown
)

// maxDetailLen bounds peer-supplied text before it reaches logs or spans.
// QUIC reason phrases and HTTP/2 debug data are meant to be short diagnostics,
// but nothing in either protocol enforces that, and both are written by the
// remote end.
const maxDetailLen = 256

// truncationMarker is appended when text is cut. Its length is reserved out of
// maxDetailLen so the returned string never exceeds the stated bound.
const truncationMarker = "[truncated]"

// sanitizePeerText makes remote-supplied text safe to log, returning at most
// maxDetailLen bytes.
//
// The reason phrase is the single most useful field here, because it is where a
// peer says why it closed, so it is bounded rather than dropped. Truncating
// caps how much a peer can push into the log stream, and stripping
// non-printables stops newlines or control sequences being used to forge log
// lines. Structured encoding already escapes these, so this is defence in
// depth rather than the only guard.
func sanitizePeerText(s string) string {
	if s == "" {
		return ""
	}
	truncated := false
	if len(s) > maxDetailLen {
		// Reserve room for the marker so the result stays within the bound
		// this function advertises.
		s = s[:maxDetailLen-len(truncationMarker)]
		truncated = true
	}
	s = strings.Map(func(r rune) rune {
		// Drop control characters and anything that failed UTF-8 decoding,
		// which includes a rune cut in half by the truncation above.
		if r == utf8.RuneError || !unicode.IsPrint(r) {
			return -1
		}
		return r
	}, s)
	if truncated {
		s += truncationMarker
	}
	return s
}

// appendPeerText adds an optional peer-supplied field to a detail string, and
// omits it entirely when the sanitized text is empty. Emitting reason="" or
// debug="" is noise that reads like the peer said nothing when in fact it sent
// no field at all.
func appendPeerText(base, key, text string) string {
	clean := sanitizePeerText(text)
	if clean == "" {
		return base
	}
	return fmt.Sprintf("%s %s=%q", base, key, clean)
}

// CloseInfo is the transport-level account of why a tunnel ended.
type CloseInfo struct {
	// Code is one of metrics.CloseCodes. Bounded, so safe as a metric label.
	Code string
	// Detail carries the peer-supplied reason where the transport provides
	// one, plus the numeric code where there is one. Unbounded, so it belongs
	// in logs and spans and never in a metric label.
	Detail string
	// Remote reports whether the peer initiated the close, where the transport
	// tells us. QUIC carries this explicitly; nothing else on this path does.
	// Nil means unknown, which must not be read as local.
	Remote *bool
}

// ClassifyCloseError turns a transport error into a bounded code plus detail.
//
// Ordering matters. A QUIC application error also satisfies net.Error, so the
// specific types are checked first; otherwise the useful code and reason get
// flattened into a bare "timeout" and the information this exists to capture
// is lost.
func ClassifyCloseError(err error) CloseInfo {
	if err == nil {
		return CloseInfo{Code: CloseCodeNone}
	}

	remote := func(b bool) *bool { return &b }

	var appErr *quic.ApplicationError
	if errors.As(err, &appErr) {
		return CloseInfo{
			Code:   CloseCodeQUICApplication,
			Detail: appendPeerText(fmt.Sprintf("code=%d", uint64(appErr.ErrorCode)), "reason", appErr.ErrorMessage),
			Remote: remote(appErr.Remote),
		}
	}
	var transportErr *quic.TransportError
	if errors.As(err, &transportErr) {
		return CloseInfo{
			Code:   CloseCodeQUICTransport,
			Detail: appendPeerText(fmt.Sprintf("code=%d", uint64(transportErr.ErrorCode)), "reason", transportErr.ErrorMessage),
			Remote: remote(transportErr.Remote),
		}
	}
	var streamErr *quic.StreamError
	if errors.As(err, &streamErr) {
		return CloseInfo{
			Code:   CloseCodeQUICStreamReset,
			Detail: fmt.Sprintf("stream=%d code=%d", int64(streamErr.StreamID), uint64(streamErr.ErrorCode)),
			Remote: remote(streamErr.Remote),
		}
	}
	var idleErr *quic.IdleTimeoutError
	if errors.As(err, &idleErr) {
		return CloseInfo{Code: CloseCodeQUICIdleTimeout, Detail: err.Error()}
	}
	var handshakeErr *quic.HandshakeTimeoutError
	if errors.As(err, &handshakeErr) {
		return CloseInfo{Code: CloseCodeQUICHandshakeTimeout, Detail: err.Error()}
	}
	var statelessErr *quic.StatelessResetError
	if errors.As(err, &statelessErr) {
		return CloseInfo{Code: CloseCodeQUICStatelessReset, Detail: err.Error(), Remote: remote(true)}
	}

	var goAwayErr http2.GoAwayError
	if errors.As(err, &goAwayErr) {
		return CloseInfo{
			Code:   CloseCodeH2GoAway,
			Detail: appendPeerText(fmt.Sprintf("code=%s last_stream=%d", goAwayErr.ErrCode, goAwayErr.LastStreamID), "debug", goAwayErr.DebugData),
			Remote: remote(true),
		}
	}
	var h2StreamErr http2.StreamError
	if errors.As(err, &h2StreamErr) {
		return CloseInfo{
			Code:   CloseCodeH2Stream,
			Detail: fmt.Sprintf("stream=%d code=%s", h2StreamErr.StreamID, h2StreamErr.Code),
		}
	}
	var h2ConnErr http2.ConnectionError
	if errors.As(err, &h2ConnErr) {
		return CloseInfo{Code: CloseCodeH2Connection, Detail: h2ConnErr.Error()}
	}

	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return CloseInfo{Code: CloseCodeEOF, Detail: err.Error(), Remote: remote(true)}
	}
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) {
		return CloseInfo{Code: CloseCodeReset, Detail: err.Error(), Remote: remote(true)}
	}
	if errors.Is(err, net.ErrClosed) {
		return CloseInfo{Code: CloseCodeClosedConn, Detail: err.Error()}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return CloseInfo{Code: CloseCodeContextCanceled, Detail: err.Error()}
	}
	// net.Error last: several cases above also satisfy it, and a bare timeout
	// is the least informative reading of any of them.
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return CloseInfo{Code: CloseCodeTimeout, Detail: err.Error()}
	}

	return CloseInfo{Code: CloseCodeUnknown, Detail: err.Error()}
}
