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
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"nvcf-grpc-proxy/proxy/consts"
	"nvcf-grpc-proxy/proxy/metrics"
	"nvcf-grpc-proxy/proxy/pool"
	"nvcf-grpc-proxy/proxy/rp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"golang.org/x/net/http2"
)

func NewWorkerConnection(requestId uuid.UUID, functionId, functionVersionId string, onActive, onInactive func()) *WorkerConnection {
	return &WorkerConnection{
		RequestId:         requestId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
		CreatedAt:         time.Now(),
		connPopulated:     make(chan struct{}),
		onActive:          onActive,
		onInactive:        onInactive,
	}
}

// SetCloseOrigin records why this tunnel is being torn down. First writer
// wins, so the original cause survives any later cascading close.
func (w *WorkerConnection) SetCloseOrigin(origin string) {
	w.closeOrigin.CompareAndSwap(nil, origin)
}

// CloseOrigin returns the recorded teardown origin, or "" if none was set.
func (w *WorkerConnection) CloseOrigin() string {
	if v, ok := w.closeOrigin.Load().(string); ok {
		return v
	}
	return ""
}

type WorkerConnection struct {
	RequestId         uuid.UUID
	FunctionId        string
	FunctionVersionId string
	// CreatedAt is when this connection entered the worker connection cache.
	// Used to report how long a tunnel stayed open when it is closed.
	CreatedAt time.Time
	// closeOrigin records which side initiated the teardown, set by whoever
	// calls onInactive. Without it the eviction handler only sees "deleted"
	// and cannot tell a client hang-up from a worker hang-up.
	closeOrigin atomic.Value
	// closeErr is the transport error that ended the tunnel, if there was one.
	// Distinct from closeOrigin: origin says which side tore down, closeErr
	// says what the transport reported while doing it.
	closeErr atomic.Pointer[errBox]
	// closedAt is when the proxy observed this tunnel stop carrying traffic,
	// recorded by whichever side noticed first: the worker transport faulting,
	// or the client connection going away.
	//
	// Deliberately not "when we finished tearing down". Teardown is our own
	// cleanup and can lag arbitrarily behind the session actually ending, and
	// folding that lag into held_for is the measurement error this field
	// exists to remove. First writer wins for the same reason: the moment the
	// tunnel stopped being useful is what matters, not the last step of the
	// cascade that follows it.
	closedAt        atomic.Pointer[time.Time]
	connSetOnce     sync.Once
	connPopulated   chan struct{}
	handler         atomic.Pointer[httputil.ReverseProxy]
	closeWorkerConn io.Closer
	onActive        func() // call this function to indicate the connection is active
	onInactive      func() // call this function to indicate the connection is idle
}

// SetCloseError records the transport error that ended this tunnel. First
// writer wins, matching SetCloseOrigin.
func (w *WorkerConnection) SetCloseError(err error) {
	if err == nil {
		return
	}
	w.closeErr.CompareAndSwap(nil, &errBox{err: err})
}

// CloseError returns the recorded transport error, or nil if none was seen.
func (w *WorkerConnection) CloseError() error {
	if b := w.closeErr.Load(); b != nil {
		return b.err
	}
	return nil
}

// MarkClosed stamps the moment this tunnel stopped carrying traffic. First
// writer wins, so a later step in the teardown cascade cannot overwrite the
// moment the session actually ended.
func (w *WorkerConnection) MarkClosed(t time.Time) {
	w.closedAt.CompareAndSwap(nil, &t)
}

// ClosedAt returns when the tunnel stopped carrying traffic, or the zero time
// if no close was stamped.
func (w *WorkerConnection) ClosedAt() time.Time {
	if t := w.closedAt.Load(); t != nil {
		return *t
	}
	return time.Time{}
}

// WaitForConnection may return without a connection if the WorkerConnection struct is closed while
// waiting. Check the return value before using the connection.
func (w *WorkerConnection) WaitForConnection(ctx context.Context) (http.Handler, bool) {
	select {
	case <-w.connPopulated:
		break
	case <-ctx.Done():
		return nil, false
	}
	handler := w.handler.Load()
	return handler, handler != nil
}

func (w *WorkerConnection) WorkerClosed() bool {
	select {
	case <-w.connPopulated:
		return w.handler.Load() == nil
	default:
		return false
	}
}

func (w *WorkerConnection) SetConnection(conn net.Conn) error {
	var err error
	set := false
	w.connSetOnce.Do(func() {
		wrapped := &CloseFuncConn{Conn: conn}
		wrapped.onClose = func() {
			w.SetCloseOrigin(metrics.CloseReasonWorkerClosed)
			w.SetCloseError(wrapped.FirstError())
			w.MarkClosed(time.Now())
			w.onInactive()
		}
		dialOnce := atomic.Bool{}
		dialContext := func(ctx context.Context, network, addr string) (net.Conn, error) {
			if !dialOnce.Swap(true) {
				w.onActive()
				// Hand out the wrapper, not the raw conn: it is what records
				// the transport error used to explain the close.
				return wrapped, nil
			}
			return nil, fmt.Errorf("can only dial once")
		}
		h1Transport := &http.Transport{
			DisableCompression: true,
			DialContext:        dialContext,
			IdleConnTimeout:    consts.Timeout,
		}
		h2Transport := &http2.Transport{
			AllowHTTP: true,
			DialTLSContext: func(ctx context.Context, network, addr string, cfg *tls.Config) (net.Conn, error) {
				return dialContext(ctx, network, addr)
			},
			IdleConnTimeout:            consts.Timeout,
			WriteByteTimeout:           consts.Timeout,
			ReadIdleTimeout:            consts.Timeout,
			StrictMaxConcurrentStreams: true,
			DisableCompression:         true,
		}

		sendCookieOnce := sync.Once{}
		requestId := w.RequestId.String()
		handler := &httputil.ReverseProxy{
			Rewrite: func(request *httputil.ProxyRequest) {
				request.Out.URL.Scheme = "http" // required for h2c with pre-dialed conn
				if request.In.ProtoMajor != 2 {
					request.Out.URL.Host = "localhost" // required for h1 transport
				}
				request.Out.Header.Set(consts.RequestIdHeaderName, requestId)
			},
			Transport:     NewProtoRoutingTransport(h1Transport, h2Transport),
			FlushInterval: -1,
			BufferPool:    pool.ByteSlice,
			ModifyResponse: func(response *http.Response) error {
				sendCookieOnce.Do(func() {
					cookie := (&http.Cookie{
						Name:  consts.RequestIdCookieName,
						Value: requestId,
					}).String()
					response.Header.Add("Set-Cookie", cookie)
				})
				response.Header.Set(consts.RequestIdHeaderName, requestId)
				return nil
			},
		}
		err = rp.InjectGrpcSupportToReverseProxy(handler)
		if err != nil {
			return
		}
		w.handler.Store(handler)
		w.closeWorkerConn = wrapped // implements io.Closer
		close(w.connPopulated)
		set = true
	})
	if !set {
		if err != nil {
			return err
		}
		return fmt.Errorf("worker connection was already registered for this request")
	}
	return nil
}

func (w *WorkerConnection) Close() error {
	w.logClosure("explicit_close")
	w.connSetOnce.Do(func() {
		close(w.connPopulated)
	})
	w.handler.Store(nil)
	var err error
	if w.closeWorkerConn != nil {
		err = w.closeWorkerConn.Close()
	}
	return err
}

func (w *WorkerConnection) logClosure(reason string) {
	logFields := []zap.Field{
		zap.String("reason", reason),
		zap.Stringer("request_id", w.RequestId),
	}

	// Only log function info if it was set
	if w.FunctionId != "" {
		logFields = append(logFields, zap.String("function_id", w.FunctionId))
	}
	if w.FunctionVersionId != "" {
		logFields = append(logFields, zap.String("function_version_id", w.FunctionVersionId))
	}

	zap.L().Info("closing worker connection", logFields...)
}

// errBox keeps the concrete type stored in an atomic.Pointer constant.
// Storing bare error values in an atomic.Value panics as soon as two different
// concrete error types are written.
type errBox struct{ err error }

type CloseFuncConn struct {
	net.Conn
	onClose func()
	// firstErr is the first transport error seen on this connection. Recorded
	// on Read and Write rather than in Close, because by the time Close runs
	// the underlying cause has usually been discarded. First writer wins: the
	// original fault is more informative than the cascade that follows it.
	firstErr atomic.Pointer[errBox]
}

func (c *CloseFuncConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.recordErr(err)
	return n, err
}

func (c *CloseFuncConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	c.recordErr(err)
	return n, err
}

func (c *CloseFuncConn) recordErr(err error) {
	if err == nil {
		return
	}
	c.firstErr.CompareAndSwap(nil, &errBox{err: err})
}

// FirstError returns the first transport error seen, or nil if the connection
// closed without one.
func (c *CloseFuncConn) FirstError() error {
	if b := c.firstErr.Load(); b != nil {
		return b.err
	}
	return nil
}

func (c *CloseFuncConn) Close() error {
	c.onClose()
	return c.Conn.Close()
}
