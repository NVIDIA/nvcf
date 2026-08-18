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
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	nverrors "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/errors"
	"github.com/go-chi/cors"
	"github.com/google/uuid"
	"github.com/jellydator/ttlcache/v3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	grpcCodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"nvcf-grpc-proxy/proxy/consts"
	"nvcf-grpc-proxy/proxy/invocation"
	"nvcf-grpc-proxy/proxy/metrics"
	"nvcf-grpc-proxy/proxy/middleware"
	"nvcf-grpc-proxy/proxy/worker"
)

type FunctionInvoker interface {
	InvokeStatefulFunction(ctx context.Context, conn net.Conn, clientAuth, functionId, functionVersionId string, existingRequestId *uuid.UUID, onWorkerAuthSet func(workerAuthToken string, requestId uuid.UUID, apiFunctionId string, apiFunctionVersionId string)) (invocation.Result, context.CancelFunc, error)
}

type workerAuthInfo struct {
	requestId         uuid.UUID
	functionId        string
	functionVersionId string
	// mintedAt lets a CONNECT report how old the token was when it arrived,
	// which is the headroom against consts.Timeout.
	mintedAt time.Time
}

// issuedTokenInfo is diagnostic only. It outlives the auth entry so a rejected
// CONNECT can distinguish a token that expired from one this pod never issued.
// It grants nothing: authentication still reads workerAuth exclusively.
type issuedTokenInfo struct {
	mintedAt time.Time
}

type StreamDirector struct {
	shuttingDown    *atomic.Bool
	workerAuth      *ttlcache.Cache[string, workerAuthInfo]  // auth -> request + function info
	issuedTokens    *ttlcache.Cache[string, issuedTokenInfo] // diagnostic only, see issuedTokenInfo
	workers         *ttlcache.Cache[workerConnectionKey, *worker.WorkerConnection]
	functionInvoker FunctionInvoker
	cors            *cors.Cors
}

// workerConnectionKey
// when a worker connects back to the proxy (this service), it sends a request id and an auth token
// specific to the connection it wants to form. Since multiple connections are allowed per request
// ID we use the connection specific token to differentiate.
type workerConnectionKey struct {
	requestId       uuid.UUID
	workerAuthToken string
	// function information does not add uniqueness to the key; it is only used for logging context
	functionId        string
	functionVersionId string
}

func NewStreamDirector(functionInvoker FunctionInvoker) *StreamDirector {
	workerAuthCache := ttlcache.New(
		// no point in the auth being valid past the client timeout
		ttlcache.WithTTL[string, workerAuthInfo](consts.Timeout),
		ttlcache.WithDisableTouchOnHit[string, workerAuthInfo](),
	)
	go workerAuthCache.Start()

	// Diagnostic record of issued tokens, deliberately outliving the auth
	// entry so a 403 can distinguish expired from never-issued. Bounded in
	// size so it cannot grow without limit.
	issuedTokenCache := ttlcache.New(
		ttlcache.WithTTL[string, issuedTokenInfo](issuedTokenRetention),
		ttlcache.WithCapacity[string, issuedTokenInfo](issuedTokenCacheCapacity),
		ttlcache.WithDisableTouchOnHit[string, issuedTokenInfo](),
	)
	go issuedTokenCache.Start()

	// Set immediately before DeleteAll in Close so the eviction handler can
	// report shutdown rather than attributing a drain to a client or worker.
	shuttingDown := &atomic.Bool{}

	cache := ttlcache.New(
		ttlcache.WithTTL[workerConnectionKey, *worker.WorkerConnection](consts.Timeout),
		ttlcache.WithLoader(ttlcache.NewSuppressedLoader(
			ttlcache.LoaderFunc[workerConnectionKey, *worker.WorkerConnection](func(c *ttlcache.Cache[workerConnectionKey, *worker.WorkerConnection], k workerConnectionKey) *ttlcache.Item[workerConnectionKey, *worker.WorkerConnection] {
				ttlUpdateLock := sync.Mutex{}
				// hold this lock while we're inserting the new connection in case onActive or onInactive gets called before the loader func returns
				ttlUpdateLock.Lock()
				defer ttlUpdateLock.Unlock()
				onActive := func() {
					// when a connection becomes active take manual control of the ttl.
					// we will be notified when the function shuts down or goes idle.
					ttlUpdateLock.Lock()
					defer ttlUpdateLock.Unlock()
					v := c.Get(k, ttlcache.WithLoader[workerConnectionKey, *worker.WorkerConnection](nil))
					if v == nil {
						zap.L().Error("tried setting connection function active but it was missing from the ttl cache", zap.Stringer("request id", k.requestId))
						return
					}
					c.Set(k, v.Value(), ttlcache.NoTTL)
				}
				onInactive := func() {
					// shut down connections that have gone idle.
					// OnEviction will close connections deleted from the cache.
					zap.L().Info("connection going inactive, removing from cache",
						zap.Stringer("request_id", k.requestId))
					ttlUpdateLock.Lock()
					defer ttlUpdateLock.Unlock()
					c.Delete(k)
				}
				newWorkerConnection := worker.NewWorkerConnection(k.requestId, k.functionId, k.functionVersionId, onActive, onInactive)
				// the new connection gets inserted here, not by the return value of the loader func.
				// this has the happy side effect that we are still holding the ttl update lock.
				conn, connAlreadyExisted := c.GetOrSet(k, newWorkerConnection)
				if connAlreadyExisted {
					zap.L().Error("worker conn already present in cache, closing new worker conn", zap.Stringer("request id", k.requestId))
					_ = newWorkerConnection.Close()
				} else {
					// Counted here rather than at dial so the open and close
					// counts balance: eviction fires for every entry that
					// makes it into the cache.
					metrics.WorkerConnectionOpenedTotal.Inc()
					metrics.WorkerConnectionsActive.Inc()
				}
				return conn
			}), nil)),
	)
	cache.OnEviction(func(ctx context.Context, reason ttlcache.EvictionReason, i *ttlcache.Item[workerConnectionKey, *worker.WorkerConnection]) {
		wc := i.Value()
		reasonStr := resolveCloseReason(reason, wc, shuttingDown.Load())
		heldFor := time.Since(wc.CreatedAt)

		metrics.WorkerConnectionClosedTotal.WithLabelValues(reasonStr).Inc()
		metrics.WorkerConnectionsActive.Dec()
		metrics.WorkerConnectionDurationSeconds.Observe(heldFor.Seconds())

		// Promoted from debug to info: this is the only place the proxy records
		// WHY it dropped a worker tunnel, and that question is routinely asked
		// during incidents. At debug it was unavailable in production exactly
		// when it was needed. The message text is unchanged so existing log
		// searches keep working.
		zap.L().Info("worker connection cache eviction triggered",
			zap.Stringer("request_id", i.Key().requestId),
			zap.String("eviction_reason", reasonStr),
			zap.String("raw_eviction_reason", mapEvictionReason(reason)),
			zap.Duration("held_for", heldFor),
			zap.String("function_id", wc.FunctionId),
			zap.String("function_version_id", wc.FunctionVersionId))

		// The eviction context is the cache's own, not the session's, so this
		// span has no parent to attach to. It carries the request id as an
		// attribute so a dropped session can still be correlated in tracing
		// without grepping logs. Name follows the service.operation convention
		// in AGENTS.md and must stay stable so dashboards do not break.
		_, span := otel.GetTracerProvider().Tracer("proxy-tracer").Start(ctx, "grpc-proxy.worker_connection_cache_eviction",
			trace.WithAttributes(
				attribute.Stringer("request_id", i.Key().requestId),
				attribute.String("eviction_reason", reasonStr),
				attribute.Float64("held_for_seconds", heldFor.Seconds()),
				attribute.String("function_id", wc.FunctionId),
				attribute.String("function_version_id", wc.FunctionVersionId),
			))
		span.End()

		_ = wc.Close()
	})
	go cache.Start()

	return &StreamDirector{
		workers:         cache,
		shuttingDown:    shuttingDown,
		issuedTokens:    issuedTokenCache,
		workerAuth:      workerAuthCache,
		functionInvoker: functionInvoker,
		cors:            cors.New(middleware.DefaultCorsOptions),
	}
}

// resolveCloseReason turns a ttlcache eviction into something actionable.
// EvictionReasonDeleted on its own is ambiguous: it covers the client hanging
// up, the worker hanging up, and a proxy drain. Whoever initiated the teardown
// records an origin on the connection first, so prefer that.
func resolveCloseReason(reason ttlcache.EvictionReason, wc *worker.WorkerConnection, shuttingDown bool) string {
	mapped := mapEvictionReason(reason)
	if mapped != metrics.CloseReasonDeleted {
		return mapped
	}
	if shuttingDown {
		return metrics.CloseReasonShutdown
	}
	if origin := wc.CloseOrigin(); origin != "" {
		return origin
	}
	// A delete with no recorded origin means a teardown path is missing
	// instrumentation. Left distinguishable on purpose so it is visible.
	return metrics.CloseReasonDeleted
}

func mapEvictionReason(reason ttlcache.EvictionReason) string {
	switch reason {
	case ttlcache.EvictionReasonExpired:
		return metrics.CloseReasonTTLExpired
	case ttlcache.EvictionReasonDeleted:
		return metrics.CloseReasonDeleted
	case ttlcache.EvictionReasonCapacityReached:
		return metrics.CloseReasonCapacity
	default:
		return metrics.CloseReasonUnknown
	}
}

const (
	// issuedTokenRetention is how long the diagnostic record of an issued
	// token is kept. Comfortably longer than consts.Timeout so an expired
	// token is still recognisable as one we issued.
	issuedTokenRetention = 15 * time.Minute
	// issuedTokenCacheCapacity bounds the diagnostic cache.
	issuedTokenCacheCapacity = 50000
)

func (s *StreamDirector) Close() error {
	// Mark first: DeleteAll evicts every entry, and without this those
	// evictions would be misreported as client or worker initiated.
	s.shuttingDown.Store(true)
	s.workers.DeleteAll()
	s.workers.Stop()
	s.workerAuth.Stop()
	s.issuedTokens.Stop()
	if s.functionInvoker != nil {
		if closer, ok := s.functionInvoker.(io.Closer); ok {
			_ = closer.Close()
		}
	}
	return nil
}

func (s *StreamDirector) RegisterWorker(requestId uuid.UUID, workerAuthToken string, functionId string, functionVersionId string, workerLink net.Conn) error {
	zap.L().Info("registering new worker connection", zap.Stringer("request id", requestId))

	key := workerConnectionKey{
		requestId:         requestId,
		workerAuthToken:   workerAuthToken,
		functionId:        functionId,
		functionVersionId: functionVersionId,
	}

	w := s.workers.Get(key)
	if w == nil {
		return fmt.Errorf("worker connection not found for request id %s", requestId)
	}
	return w.Value().SetConnection(workerLink)
}

func (s *StreamDirector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	metrics.ActiveHttpRequestsTotal.Inc()
	defer metrics.ActiveHttpRequestsTotal.Dec()
	err := s.serveStatefulRequest(w, r)
	if err != nil {
		// a session is not going to become valid, so ask the client to delete their cookie before retrying
		if errors.Is(err, invocation.ErrSessionNotFound) {
			cookie := (&http.Cookie{
				Name:   consts.RequestIdCookieName,
				Value:  "",
				MaxAge: -1,
			}).String()
			w.Header().Add("Set-Cookie", cookie)
		}
		// only apply the cors handler on error since the grpc proxy is producing the response
		// without the end inference container being able to produce its own cors headers.
		s.cors.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if mediaType == "application/grpc" {
				grpcError(w, err)
				return
			}
			httpError(w, err)
		})).ServeHTTP(w, r)
	}
}

func (s *StreamDirector) serveStatefulRequest(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	conn := worker.ConnFromCtx(ctx)

	conn.InitStatefulSession(ctx)
	conn.SetSessionSpanAsParent(ctx)
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(attribute.String("content-type", r.Header.Get("Content-Type")))

	// parse the WebSocket protocol headers once for efficiency
	wsProtocolHeaders := parseWebSocketProtocol(r.Header)

	// the client's auth is checked by the NVCF API. subsequent requests to the same function on
	// this tcp connection don't need to be checked against the NVCF API. if a new function is
	// called or the worker for this function dies then the auth will be checked again.
	authHeader := getHeaderWithWebSocketProtocolFallback(r.Header, "authorization", wsProtocolHeaders)
	auth := strings.TrimPrefix(authHeader, "Bearer ")
	// if it's a browser websocket request, they can't send us "Bearer {auth}" because it has a space,
	// which is outside the allowable charset. allow this only for these clients. all other clients
	// must correctly send us the token type.
	if auth == "" || (wsProtocolHeaders == nil && !strings.HasPrefix(authHeader, "Bearer ")) {
		err := nverrors.NewNVError(fmt.Errorf("no authorization was passed in the metadata")).WithCode(grpcCodes.Unauthenticated)
		span.RecordError(err)
		return err
	}

	functionId := getHeaderWithWebSocketProtocolFallback(r.Header, "function-id", wsProtocolHeaders)
	if functionId == "" {
		err := nverrors.NewNVError(fmt.Errorf("no function-id was passed in the metadata")).WithCode(grpcCodes.InvalidArgument)
		span.RecordError(err)
		return err
	}
	functionVersionId := getHeaderWithWebSocketProtocolFallback(r.Header, "function-version-id", wsProtocolHeaders)
	span.SetAttributes(attribute.String("function_id", functionId),
		attribute.String("function_version_id", functionVersionId))

	requestId := getRequestIdFromHeaderOrCookie(r, wsProtocolHeaders)

	// add context to the logger for this request so that we don't repeat ourselves
	reqLogger := zap.L().With(zap.String("function", functionId),
		zap.String("function version", functionVersionId),
		zap.Stringer("request id", requestId),
		zap.String("path", r.URL.Path))

	workerConn, err := s.getAndInitWorkerConnection(ctx, conn, auth, functionId, functionVersionId, requestId)
	if err != nil {
		return spanError(span, err)
	}
	reqLogger.Info("directing client to worker connection", zap.Stringer("request id", workerConn.RequestId))

	upstreamHandler, ok := workerConn.WaitForConnection(ctx)
	if !ok {
		// maps to 504 if non-grpc
		err := nverrors.NewNVError(fmt.Errorf("failed to establish link to worker")).WithCode(grpcCodes.DeadlineExceeded)
		return spanError(span, err)
	}
	reqLogger.Info("client directed to worker connection", zap.Stringer("request id", workerConn.RequestId))

	upstreamHandler.ServeHTTP(w, r)
	return nil
}

func parseWebSocketProtocol(headers http.Header) http.Header {
	protocolHeaders := headers.Values("Sec-WebSocket-Protocol")
	if len(protocolHeaders) > 0 {
		wsHeaders := make(http.Header)
		for _, h := range protocolHeaders {
			// split on comma to get the list of headers
			for _, headerValue := range strings.Split(h, ",") {
				// both "," and ", " are valid separators
				headerValue = strings.TrimPrefix(headerValue, " ")
				// we're doing dot separated kvs for the headers. thank you browsers.
				if k, v, ok := strings.Cut(headerValue, "."); ok {
					wsHeaders.Add(k, v)
				}
			}
		}
		return wsHeaders
	}
	return nil
}

func getHeaderWithWebSocketProtocolFallback(headers http.Header, headerName string, wsProtocolHeaders http.Header) string {
	header := headers.Get(headerName)
	if header == "" {
		header = wsProtocolHeaders.Get(headerName)
	}
	return header
}

func getRequestIdFromHeaderOrCookie(r *http.Request, wsProtocolHeaders http.Header) *uuid.UUID {
	requestId := getHeaderWithWebSocketProtocolFallback(r.Header, consts.RequestIdHeaderName, wsProtocolHeaders)
	if requestId == "" {
		requestIdCookie, err := r.Cookie(consts.RequestIdCookieName)
		if err == nil {
			requestId = requestIdCookie.Value
		}
	}
	parsed, err := uuid.Parse(requestId)
	if err != nil {
		return nil
	}
	return &parsed
}

func httpError(w http.ResponseWriter, err error) {
	httpStatus := http.StatusInternalServerError
	if s, ok := status.FromError(err); ok {
		httpStatus = GrpcCodeToHttpStatusCode(s.Code())
	}
	w.WriteHeader(httpStatus)
	_, _ = w.Write([]byte(err.Error()))
}

func grpcError(w http.ResponseWriter, err error) {
	code := grpcCodes.Internal
	if s, ok := status.FromError(err); ok {
		code = s.Code()
	}
	w.Header().Set("grpc-status", strconv.Itoa(int(code)))
	w.Header().Set("grpc-message", err.Error())
	w.Header().Set("Content-Type", "application/grpc")
	w.WriteHeader(http.StatusOK)
}

func spanError(span trace.Span, err error) error {
	span.RecordError(err)
	span.SetStatus(codes.Error, "")
	zap.L().Error("recording span error", zap.Error(err))
	return err
}

func (s *StreamDirector) getAndInitWorkerConnection(ctx context.Context, conn *worker.ConnectionTrackingConn, auth, functionId, functionVersionId string, requestId *uuid.UUID) (*worker.WorkerConnection, error) {
	return conn.InitWorkerConn(functionId, functionVersionId, func() (*worker.WorkerConnection, error) {
		// one connection + function routing gets one work request so we don't request more than
		// one worker if multiple RPCs are sent on the connection before the first worker appears.

		// Capture API-provided function info in closure
		var apiFunctionId, apiFunctionVersionId string

		invokeResponse, cancelInvokingWorker, err := s.functionInvoker.InvokeStatefulFunction(ctx, conn, auth, functionId, functionVersionId, requestId, func(workerAuthToken string, requestId uuid.UUID, apiFunc string, apiFuncVersion string) {
			// Populate workerAuth cache BEFORE worker is notified (atomicity guarantee)
			now := time.Now()
			s.workerAuth.Set(workerAuthToken, workerAuthInfo{
				requestId:         requestId,
				functionId:        apiFunc,
				functionVersionId: apiFuncVersion,
				mintedAt:          now,
			}, ttlcache.DefaultTTL)
			// Diagnostic shadow record, longer lived than the auth entry, so a
			// later rejection can say "expired N seconds ago" instead of just
			// "not found". Never consulted when granting access.
			s.issuedTokens.Set(workerAuthToken, issuedTokenInfo{mintedAt: now}, ttlcache.DefaultTTL)
			metrics.WorkerTokenIssuedTotal.Inc()
			// Capture the API response values
			apiFunctionId = apiFunc
			apiFunctionVersionId = apiFuncVersion
		})
		if err != nil {
			zap.L().Warn("failed to open stateful work request", zap.Error(err), zap.String("function id", functionId), zap.Stringer("request id", requestId))
			return nil, fmt.Errorf("failed to open stateful work request: %w", err)
		}

		workerConnection := s.workers.Get(workerConnectionKey{
			requestId:         invokeResponse.RequestId,
			workerAuthToken:   invokeResponse.WorkerAuthorizationToken,
			functionId:        apiFunctionId,
			functionVersionId: apiFunctionVersionId,
		}).Value()
		if cancelInvokingWorker != nil {
			go func() {
				// once a connection shows up or the context goes away we should stop looking for a worker
				workerConnection.WaitForConnection(ctx)
				cancelInvokingWorker()
			}()
		}
		return workerConnection, nil
	})
}

func GrpcCodeToHttpStatusCode(code grpcCodes.Code) int {
	switch code {
	case grpcCodes.OK:
		return http.StatusOK
	case grpcCodes.InvalidArgument:
		return http.StatusBadRequest
	case grpcCodes.DeadlineExceeded:
		return http.StatusGatewayTimeout
	case grpcCodes.NotFound:
		return http.StatusNotFound
	case grpcCodes.AlreadyExists:
		return http.StatusConflict
	case grpcCodes.PermissionDenied:
		return http.StatusForbidden
	case grpcCodes.Unauthenticated:
		return http.StatusUnauthorized
	case grpcCodes.ResourceExhausted:
		return http.StatusTooManyRequests
	case grpcCodes.Unimplemented:
		return http.StatusNotImplemented
	case grpcCodes.Internal:
		return http.StatusInternalServerError
	case grpcCodes.Unavailable:
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}
