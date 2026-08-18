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
	"syscall"

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
			Detail: fmt.Sprintf("code=%d reason=%q", uint64(appErr.ErrorCode), appErr.ErrorMessage),
			Remote: remote(appErr.Remote),
		}
	}
	var transportErr *quic.TransportError
	if errors.As(err, &transportErr) {
		return CloseInfo{
			Code:   CloseCodeQUICTransport,
			Detail: fmt.Sprintf("code=%d reason=%q", uint64(transportErr.ErrorCode), transportErr.ErrorMessage),
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
			Detail: fmt.Sprintf("code=%s last_stream=%d debug=%q", goAwayErr.ErrCode, goAwayErr.LastStreamID, goAwayErr.DebugData),
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
