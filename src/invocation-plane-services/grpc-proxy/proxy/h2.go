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
package proxy

import (
	"context"
	golibversion "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/version"
	"github.com/hellofresh/health-go/v5"
	"go.uber.org/zap"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"math"
	"net"
	"net/http"
	"nvcf-grpc-proxy/proxy/middleware"
	"nvcf-grpc-proxy/proxy/worker"
)

// newProxyMux builds the HTTP mux served by the http/2 listener: proxied
// traffic on "/", the health check on "/health", and the GET /info
// build-version endpoint.
func newProxyMux(director *StreamDirector, healthManager *health.Health) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/", director)
	// TODO only route to this health if there are no function id headers
	mux.HandleFunc("/health", healthManager.HandlerFunc)
	mux.HandleFunc("/info", golibversion.Handler().ServeHTTP)
	return mux
}

func createHttp2Server(director *StreamDirector, addr string, healthManager *health.Health) *InterceptedHttpServer {
	mux := newProxyMux(director, healthManager)
	corsMux := middleware.Cors(mux)
	tracedMux := middleware.ApplyMiddleware(corsMux, "http/2 listener")
	return &InterceptedHttpServer{http.Server{
		Addr: addr,
		Handler: h2c.NewHandler(tracedMux, &http2.Server{
			MaxConcurrentStreams: math.MaxInt32,
		}),
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			return worker.CtxWithConn(ctx, c.(*worker.ConnectionTrackingConn))
		},
	}}
}

type InterceptedHttpServer struct {
	http.Server
}

func (s *InterceptedHttpServer) ListenAndServe() error {
	listener, err := net.Listen("tcp", s.Addr)
	if err != nil {
		return err
	}
	return s.Serve(listener)
}

// Serve wraps an already-bound listener with the connection-tracking
// interceptor and hands it to the embedded http.Server. Callers that
// need to discover the bound address before serving (eg tests using
// ephemeral ports) bind a listener themselves and pass it here; this
// keeps the interceptor wrap in one place instead of duplicating it.
func (s *InterceptedHttpServer) Serve(l net.Listener) error {
	l = ListenerInterceptor{Listener: l, InterceptFunc: func(conn net.Conn) net.Conn {
		zap.L().Debug("new http/2 listener connection")
		return worker.NewConnectionTrackingConn(conn)
	}}
	return s.Server.Serve(l)
}

type ListenerInterceptor struct {
	net.Listener
	InterceptFunc func(conn net.Conn) net.Conn
}

func (l ListenerInterceptor) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err == nil {
		conn = l.InterceptFunc(conn)
	}
	return conn, err
}
