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
	"nvcf-grpc-proxy/proxy/pool"
	"nvcf-grpc-proxy/proxy/rp"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"golang.org/x/net/http2"
)

func NewWorkerConnection(requestId uuid.UUID, functionId, functionVersionId string, onActive, onInactive func()) *WorkerConnection {
	return &WorkerConnection{
		RequestId:         requestId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
		connPopulated:     make(chan struct{}),
		onActive:          onActive,
		onInactive:        onInactive,
	}
}

type WorkerConnection struct {
	RequestId         uuid.UUID
	FunctionId        string
	FunctionVersionId string
	connSetOnce       sync.Once
	connPopulated     chan struct{}
	handler           atomic.Pointer[httputil.ReverseProxy]
	closeWorkerConn   io.Closer
	onActive          func() // call this function to indicate the connection is active
	onInactive        func() // call this function to indicate the connection is idle
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
		conn := CloseFuncConn{Conn: conn, onClose: w.onInactive}
		dialOnce := atomic.Bool{}
		dialContext := func(ctx context.Context, network, addr string) (net.Conn, error) {
			if !dialOnce.Swap(true) {
				w.onActive()
				return conn, nil
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
		w.closeWorkerConn = conn // implements io.Closer
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

type CloseFuncConn struct {
	net.Conn
	onClose func()
}

func (c CloseFuncConn) Close() error {
	c.onClose()
	return c.Conn.Close()
}
