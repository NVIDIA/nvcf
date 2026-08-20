/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/goccy/go-json"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/quic-go/quic-go/http3"
	"go.opentelemetry.io/contrib/instrumentation/net/http/httptrace/otelhttptrace"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/protobuf/proto"

	semconv "go.opentelemetry.io/otel/semconv/v1.20.0"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/metrics/nvcf"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvcf"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proxy/buffconn"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proxy/handlerisolationconn"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proxy/quicconn"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proxy/rp"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/utils"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/utils/middleware"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/utils/pool"
)

const (
	retryConnectDelay          = 10 * time.Millisecond
	reconnectOnErrWaitDuration = 30 * time.Second
)

var ErrAuth = backoff.Permanent(errors.New("permanent auth error"))

type handlerContextKey struct{}

type HttpProxy struct {
	nc                *nats.Conn          // listening for reconnect messages
	js                jetstream.JetStream // for registering and deleting session info
	functionId        string
	functionVersionId string

	h3 *h3ConnectionCache

	handler            *http.Server
	handlerPool        handlerisolationconn.HandlerPool
	listener           *PushListener
	closed             atomic.Bool
	disconnectCallback func(context.Context, string, error)
}

func newHandler(rewriteFunc func(*httputil.ProxyRequest), modifyResponseFunc func(*http.Response) error, additionalMiddleware func(http.Handler) http.Handler) (http.Handler, error) {
	dialer := net.Dialer{Timeout: 1 * time.Second}
	proxy := &httputil.ReverseProxy{
		Rewrite: rewriteFunc,
		Transport: NewProtoRoutingTransport(
			&http.Transport{DialContext: dialer.DialContext, IdleConnTimeout: 30 * time.Second},
			&http2.Transport{AllowHTTP: true, DialTLSContext: func(ctx context.Context, network, addr string, cfg *tls.Config) (net.Conn, error) {
				return dialer.DialContext(ctx, network, addr)
			}, IdleConnTimeout: 30 * time.Second}),
		FlushInterval:  -1,
		BufferPool:     pool.ByteSlice32k,
		ModifyResponse: modifyResponseFunc,
	}

	err := rp.InjectGrpcSupportToReverseProxy(proxy)
	if err != nil {
		return nil, err
	}

	var httpHandler http.Handler = proxy
	if additionalMiddleware != nil {
		httpHandler = additionalMiddleware(proxy)
	}
	return httpHandler, nil
}

func NewHttpProxy(nc *nats.Conn, js jetstream.JetStream, functionId, functionVersionId string, rewriteFunc func(*httputil.ProxyRequest), modifyResponseFunc func(*http.Response) error, additionalMiddleware func(http.Handler) http.Handler, disconnectCallback func(context.Context, string, error)) (*HttpProxy, error) {
	handlerPool := handlerisolationconn.NewHandlerPool(func() http.Handler {
		handler, err := newHandler(rewriteFunc, modifyResponseFunc, additionalMiddleware)
		if err != nil {
			zap.L().Error("failed to create handler", zap.Error(err))
			return nil
		}
		return handler
	})

	// Set default disconnect callback if none was passed in.
	if disconnectCallback == nil {
		disconnectCallback = func(ctx context.Context, reqId string, err error) {
			if err != nil {
				zap.L().Debug("connection closed with error. waiting for reconnects.", zap.String("req id", reqId), zap.Error(err))
				utils.SleepWithContext(ctx, reconnectOnErrWaitDuration)
			}
		}
	}

	h := &http.Server{
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			type getHandler interface {
				GetHandler() http.Handler
			}
			type unwrapConn interface {
				Unwrap() net.Conn
			}
			for {
				handler, ok := c.(getHandler)
				if ok {
					return context.WithValue(ctx, handlerContextKey{}, handler.GetHandler())
				}
				conn, ok := c.(unwrapConn)
				if !ok {
					break
				}
				c = conn.Unwrap()
			}
			return ctx
		},
		Handler: h2c.NewHandler(middleware.ApplyMiddleware(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			handler, ok := request.Context().Value(handlerContextKey{}).(http.Handler)
			if !ok {
				zap.L().Error("handler not found or invalid", zap.Bool("ok", ok), zap.Any("handler", handler))
				http.Error(writer, "handler not found or invalid", http.StatusInternalServerError)
				return
			}
			handler.ServeHTTP(writer, request)
		}), "stateful session"), &http2.Server{
			MaxConcurrentStreams: math.MaxInt32, // must be kept as max int streams for compatibility
		}),
	}
	l := NewPushListener()
	p := &HttpProxy{
		nc:                 nc,
		js:                 js,
		functionId:         functionId,
		functionVersionId:  functionVersionId,
		handler:            h,
		handlerPool:        handlerPool,
		listener:           l,
		disconnectCallback: disconnectCallback,
		h3:                 createH3RoundTripper(),
	}
	go func() {
		err := h.Serve(l)
		if err != nil {
			if p.closed.Load() && errors.Is(err, http.ErrServerClosed) {
				return
			}
			zap.L().Error("failed to listen to local proxy server", zap.Error(err))
		}
	}()
	return p, nil
}

func (p *HttpProxy) Close() error {
	p.closed.Store(true)
	defer p.h3.Close()
	return p.handler.Close()
}

// sessionConns tracks the connections serving one stateful session.
//
// The reconnect listener can register a connection at any moment, including
// the instant the last existing one finishes. A sync.WaitGroup cannot express
// that safely: the counter would go from zero back to one while Wait was
// already returning, which is the misuse the race detector reports. Holding a
// WaitGroup reference for the listener itself does not work either, because
// the listener only exits once the session context is cancelled and that
// cancellation happens after the wait returns, so the two deadlock.
//
// Registering a connection and declaring the session over therefore happen
// under one lock, and the listener is never counted.
type sessionConns struct {
	mu     sync.Mutex
	cond   *sync.Cond
	active int
	sealed bool
}

func newSessionConns() *sessionConns {
	s := &sessionConns{}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// add registers a connection, reporting false once the session has been sealed
// and no further connections will be served.
func (s *sessionConns) add() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sealed {
		return false
	}
	s.active++
	return true
}

func (s *sessionConns) done() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active--
	if s.active == 0 {
		s.cond.Broadcast()
	}
}

// waitAndSeal blocks until every registered connection has finished, then seals
// under the same lock so a reconnect cannot register after the session has been
// declared over.
func (s *sessionConns) waitAndSeal() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.active > 0 {
		s.cond.Wait()
	}
	s.sealed = true
}

func (p *HttpProxy) Proxy(ctx context.Context, work *pb.WorkerInvokeFunctionRequest, trackingRegion string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ctx, span := otel.GetTracerProvider().Tracer("nvcf-worker-lib").Start(ctx, "Proxy")
	defer span.End()
	zap.L().Info("connecting to nvcf stateful proxy", zap.String("req id", work.RequestId))

	// start looking for reconnects asap, but we don't want to block on ack of first publish as
	// that could penalise users not using reconnects. may need to revisit if the registration
	// doesn't make it in time.
	go p.keepaliveReconnectRegistration(ctx, work, trackingRegion)

	// exit once all connections have completed successfully,
	// or 30 seconds after the last connection fails in order to wait for reconnects
	var lastConnErr atomic.Pointer[error]
	conns := newSessionConns()

	// Subscribe before the first CONNECT, not after it. The proxy decides
	// whether a session is still alive by whether anything is subscribed to
	// this subject, so any window where the session exists but the
	// subscription does not is a window where a rejoin can be misread as a
	// dead session. Subscribing here closes that window for the whole of
	// session establishment, including the CONNECT retries below.
	subscription, err := p.nc.SubscribeSync("stateful_session.reconnect." + work.RequestId)
	if err != nil {
		// Not fatal. Without the subscription the session cannot be rejoined,
		// but it can still serve the connection it is about to open.
		zap.L().Error("failed to listen for stateful session reconnects", zap.String("req id", work.RequestId), zap.Error(err))
	} else {
		go p.serveStatefulReconnects(ctx, span, work, subscription, conns, &lastConnErr)
	}

	clientConn, err := getClientConnFromProxy(ctx, work, p.h3)
	if err != nil {
		return traceError(span, err)
	}
	handlerConn, err := handlerisolationconn.NewHandlerConn(clientConn, p.handlerPool)
	if err != nil {
		return traceError(span, err)
	}
	zap.L().Info("connected to nvcf stateful proxy", zap.String("req id", work.RequestId))
	nvcf.StatefulProxySuccessCounter.Inc()

	defer func() {
		zap.L().Info("stateful work request shutting down", zap.String("req id", work.RequestId))
	}()

	conns.add()
	go func() {
		defer conns.done()
		err := p.listener.ServeConn(handlerConn)
		if err != nil {
			_ = handlerConn.Close()
		}
		err = traceError(span, err)
		lastConnErr.Store(&err)
		zap.L().Info("connection closed. triggering callback.", zap.String("req id", work.RequestId))
		p.disconnectCallback(ctx, work.RequestId, err)
	}()

	conns.waitAndSeal()
	if err := lastConnErr.Load(); err != nil {
		return *err
	}
	return nil
}

func (p *HttpProxy) serveStatefulReconnects(ctx context.Context, span trace.Span, work *pb.WorkerInvokeFunctionRequest, subscription *nats.Subscription, conns *sessionConns, lastConnErr *atomic.Pointer[error]) {
	zap.L().Info("listening for stateful reconnects", zap.String("req id", work.RequestId))
	defer func() { _ = subscription.Unsubscribe() }()
	for ctx.Err() == nil {
		var msg *nats.Msg
		err := backoff.Retry(func() error {
			nextMsg, err := subscription.NextMsgWithContext(ctx)
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					zap.L().Warn("failed to get next stateful session reconnect message", zap.String("req id", work.RequestId), zap.Error(err))
				}
				return err
			}
			msg = nextMsg
			return nil
		}, backoff.WithContext(backoff.NewExponentialBackOff(backoff.WithMaxElapsedTime(0)), ctx))
		if err != nil {
			return
		}
		var work pb.WorkerInvokeFunctionRequest
		err = proto.Unmarshal(msg.Data, &work)
		if err != nil {
			zap.L().Warn("malformed stateful session reconnect message", zap.String("req id", work.RequestId), zap.Error(err))
			continue
		}
		// Acknowledge receipt, as the polling listener does. The proxy uses
		// this to tell a live session from one whose worker is gone; without
		// it the proxy has to fall back on subscription interest alone and
		// waits out its probe deadline on every rejoin. A failed ack is not
		// a reason to drop the reconnect: the proxy treats an unanswered
		// probe as live, so carry on and serve it.
		if err := msg.Respond(nil); err != nil {
			zap.L().Warn("failed to ack stateful session reconnect", zap.String("req id", work.RequestId), zap.Error(err))
		}
		if !conns.add() {
			// The session finished while this reconnect was in flight, so
			// there is nothing left to attach it to. Stop listening rather
			// than serve a connection nobody is waiting on.
			zap.L().Info("stateful session already finished, ignoring reconnect", zap.String("req id", work.RequestId))
			return
		}
		go func() {
			defer conns.done()
			clientConn, err := getClientConnFromProxy(ctx, &work, p.h3)
			if err != nil {
				_ = traceError(span, err)
				return
			}
			handlerConn, err := handlerisolationconn.NewHandlerConn(clientConn, p.handlerPool)
			if err != nil {
				_ = traceError(span, err)
				return
			}
			zap.L().Info("connected to nvcf stateful proxy (reconnect)", zap.String("req id", work.RequestId))
			err = p.listener.ServeConn(handlerConn)
			if err != nil {
				_ = handlerConn.Close()
			}
			err = traceError(span, err)
			lastConnErr.Store(&err)
			zap.L().Info("connection closed. triggering callback.", zap.String("req id", work.RequestId))
			p.disconnectCallback(ctx, work.RequestId, err)
		}()
	}
}

func (p *HttpProxy) keepaliveReconnectRegistration(ctx context.Context, work *pb.WorkerInvokeFunctionRequest, trackingRegion string) {
	subject := fmt.Sprintf("stateful_session.lookup.%s.%s.%s", trackingRegion, p.functionVersionId, work.RequestId)
	streamName := fmt.Sprintf("stateful_session_lookup_%s", trackingRegion)
	for ctx.Err() == nil {
		_ = backoff.Retry(func() error {
			body, err := proto.Marshal(&pb.StatefulSessionTracking{
				FunctionId:        p.functionId,
				FunctionVersionId: p.functionVersionId,
				InvokerNcaId:      work.NcaId,
			})
			if err != nil {
				return err
			}
			_, err = p.js.Publish(ctx, subject, body)
			return err
		}, backoff.WithContext(backoff.NewExponentialBackOff(backoff.WithMaxElapsedTime(0)), ctx))
		utils.SleepWithContext(ctx, 20*time.Minute)
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := PurgeFromStream(ctx, p.nc, streamName, jetstream.WithPurgeSubject(subject))
		if err != nil {
			zap.L().Warn("failed to purge stateful session reconnect registration", zap.Error(err))
		}
	}()
}

// PurgeFromStream manually purges from a stream without having to first look up the stream
func PurgeFromStream(ctx context.Context, nc *nats.Conn, name string, opts ...jetstream.StreamPurgeOpt) error {
	var purgeReq jetstream.StreamPurgeRequest
	for _, opt := range opts {
		if err := opt(&purgeReq); err != nil {
			return err
		}
	}
	req, err := json.Marshal(purgeReq)
	if err != nil {
		return err
	}
	purgeSubject := jetstream.DefaultAPIPrefix + "STREAM.PURGE." + name
	_, err = nc.RequestWithContext(ctx, purgeSubject, req)
	return err
}

func getClientConnFromProxy(ctx context.Context, work *pb.WorkerInvokeFunctionRequest, h3 *h3ConnectionCache) (net.Conn, error) {
	// try a few times in case the proxy did not get the api back through the api invoke response
	// yet or in case of connection issues
	var clientConn net.Conn
	err := backoff.Retry(func() error {
		var err error
		for _, connectionConfig := range work.StatefulConfig.ConnectionConfigs {
			switch config := connectionConfig.GetConfig().(type) {
			case *pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_Http1Config:
				clientConn, err = tcpConnect(ctx, work.RequestId, config.Http1Config)
				if err != nil {
					zap.L().Warn("tcp connection attempt failed", zap.Error(err))
				}
			case *pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_Http3Config:
				clientConn, err = quicConnect(ctx, work.RequestId, config.Http3Config, h3)
				if err != nil {
					zap.L().Warn("quic connection attempt failed", zap.Error(err))
				}
			case nil:
				continue
			default:
				zap.L().Warn("unknown config", zap.Any("config", connectionConfig))
				err = errors.New("unknown CONNECT config")
			}
			if err == nil {
				break
			}
		}
		if err == nil && clientConn == nil {
			err = errors.New("no connection available")
		}
		return err
	}, backoff.WithContext(backoff.WithMaxRetries(backoff.NewExponentialBackOff(backoff.WithInitialInterval(retryConnectDelay)), 5), ctx))
	return clientConn, err
}

func traceError(span trace.Span, err error) error {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "")
	}
	return err
}

func quicConnect(ctx context.Context, requestId string, connectionConfig *pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_HTTP3ConnectionConfig, h3 *h3ConnectionCache) (net.Conn, error) {
	tracer := otel.GetTracerProvider().Tracer("nvcf-worker-lib")
	ctx, span := tracer.Start(ctx, "CONNECT /v1/proxy",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("proxy-type", "quic"),
			semconv.HTTPURL(connectionConfig.ProxyURI),
		))
	defer span.End()

	// cancel the request if we get no response for 3s
	// but if we do get a response don't cancel unless the parent ctx cancels
	ctx, cancel := context.WithCancel(ctx)
	// don't cancel request earlier than request timeout so we can detect closed connections
	timeout := time.AfterFunc(h3.wrappedTransport.QUICConfig.MaxIdleTimeout+500*time.Millisecond, cancel)
	// envoy does not match on CONNECT with our config, and this is not a standards compliant
	// CONNECT call anyway, so we will use the POST method instead.
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, connectionConfig.ProxyURI, http.NoBody)
	if err != nil {
		timeout.Stop()
		cancel()
		return nil, traceError(span, err)
	}
	request.ContentLength = -1
	request.Header.Set("Authorization", "Bearer "+connectionConfig.ProxyAuthorizationToken)
	request.Header.Set("X-Request-ID", requestId)
	otelhttptrace.Inject(ctx, request)
	parsedProxyURI, err := url.Parse(connectionConfig.ProxyURI)
	if err != nil {
		timeout.Stop()
		cancel()
		return nil, traceError(span, err)
	}
	host := parsedProxyURI.Host
	if parsedProxyURI.Port() == "" {
		if parsedProxyURI.Scheme == "https" {
			host += ":443"
		} else {
			host += ":80"
		}
	}

	cl, isReused, err := h3.getDialedClient(ctx, host)
	if err != nil {
		zap.L().Warn("failed to dial host", zap.String("host", host), zap.Error(err))
		timeout.Stop()
		cancel()
		return nil, traceError(span, err)
	}
	// manually sends an http3 request and reads the response but still allows access to the
	// http3 Stream for re-use as a net.Conn byte stream.
	roundTripFunc := func() (*http3.RequestStream, *http.Response, error) {
		stream, err := cl.clientConn.OpenRequestStream(ctx)
		if err != nil {
			return nil, nil, err
		}
		err = stream.SendRequestHeader(request)
		if err != nil {
			_ = stream.Close()
			return nil, nil, err
		}
		response, err := stream.ReadResponse()
		if err != nil {
			_ = stream.Close()
			return nil, nil, err
		}
		return stream, response, nil
	}
	stream, response, err := roundTripFunc()
	timeout.Stop()
	if err != nil {
		cl.useCount.Add(-1)
		// request aborted due to context cancellation
		if request.Context().Err() != nil {
			cancel()
			return nil, err
		}

		// Retry the request on a new connection if:
		// 1. it was sent on a reused connection,
		// 2. this connection is now closed,
		// 3. and the error is a timeout error.
		if cl.conn.Context().Err() != nil {
			zap.L().Warn("tried to use closed quic connection", zap.String("host", host), zap.Error(err))
			cl.removeFromCache()
			if isReused {
				var nerr net.Error
				if errors.As(err, &nerr) && nerr.Timeout() {
					context.AfterFunc(ctx, cancel)
					zap.L().Info("retrying fresh quic connection", zap.String("host", host))
					return quicConnect(ctx, requestId, connectionConfig, h3)
				}
			}
		}
		cancel()
		return nil, traceError(span, err)
	}

	if response.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		err := fmt.Errorf("unexpected status %d from CONNECT proxy: %s", response.StatusCode, string(errBody))
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			err = errors.Join(err, ErrAuth)
		}
		cancel()
		cl.useCount.Add(-1)
		return nil, traceError(span, err)
	}
	// purposely not closing the body, we're going to ignore the response body and use the stream directly
	// this is what hijacking used to do but hijacking is no longer supported on client requests
	conn := quicconn.NewHttp3StreamConn(stream)
	context.AfterFunc(ctx, func() { // rely on parent cancellation only
		cancel()
		cl.useCount.Add(-1)
	})
	return conn, nil
}

func tcpConnect(ctx context.Context, requestId string, connectionConfig *pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_HTTP1ConnectionConfig) (net.Conn, error) {
	tracer := otel.GetTracerProvider().Tracer("nvcf-worker-lib")
	ctx, span := tracer.Start(ctx, "CONNECT /v1/proxy",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("proxy-type", "tcp"),
			semconv.HTTPURL(connectionConfig.ProxyURI),
		))
	defer span.End()

	request, err := http.NewRequestWithContext(ctx, http.MethodConnect, connectionConfig.ProxyURI, http.NoBody)
	if err != nil {
		return nil, traceError(span, err)
	}
	request.ContentLength = -1
	request.Header.Set("Authorization", "Bearer "+connectionConfig.ProxyAuthorizationToken)
	request.Header.Set("X-Request-ID", requestId)
	otelhttptrace.Inject(ctx, request)

	proxy, err := url.Parse(connectionConfig.ProxyURI)
	if err != nil {
		return nil, traceError(span, err)
	}
	proxyAddr := proxy.Host
	if proxy.Port() == "" {
		proxyAddr = net.JoinHostPort(proxyAddr, "80")
	}

	c, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		return nil, fmt.Errorf("dialing proxy %q failed: %v", proxyAddr, err)
	}

	err = request.Write(c)
	if err != nil {
		_ = c.Close()
		return nil, traceError(span, err)
	}

	br := bufio.NewReader(c)
	response, err := http.ReadResponse(br, nil)
	if err != nil {
		_ = c.Close()
		return nil, traceError(span, err)
	}
	if response.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		_ = c.Close()
		err := fmt.Errorf("unexpected status %d from CONNECT proxy: %s", response.StatusCode, string(errBody))
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			err = errors.Join(err, ErrAuth)
		}
		return nil, traceError(span, err)
	}

	// purposely not closing the body, we're going to ignore the response body and use the stream directly
	return buffconn.NewBufConn(c, br), nil
}
