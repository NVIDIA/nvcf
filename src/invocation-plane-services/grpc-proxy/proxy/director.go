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

	nverrors "github.com/NVIDIA/nvcf-go/pkg/nvkit/errors"
	"github.com/go-chi/cors"
	"github.com/google/uuid"
	"github.com/jellydator/ttlcache/v3"
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
}

type StreamDirector struct {
	workerAuth      *ttlcache.Cache[string, workerAuthInfo] // auth -> request + function info
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
				}
				return conn
			}), nil)),
	)
	cache.OnEviction(func(ctx context.Context, reason ttlcache.EvictionReason, i *ttlcache.Item[workerConnectionKey, *worker.WorkerConnection]) {
		reasonStr := mapEvictionReason(reason)
		wc := i.Value()
		zap.L().Debug("worker connection cache eviction triggered",
			zap.Stringer("request_id", i.Key().requestId),
			zap.String("eviction_reason", reasonStr),
			zap.String("function_id", wc.FunctionId),
			zap.String("function_version_id", wc.FunctionVersionId))
		_ = wc.Close()
	})
	go cache.Start()

	return &StreamDirector{
		workers:         cache,
		workerAuth:      workerAuthCache,
		functionInvoker: functionInvoker,
		cors:            cors.New(middleware.DefaultCorsOptions),
	}
}

func mapEvictionReason(reason ttlcache.EvictionReason) string {
	switch reason {
	case ttlcache.EvictionReasonExpired:
		return "ttl_expired"
	case ttlcache.EvictionReasonDeleted:
		return "deleted"
	case ttlcache.EvictionReasonCapacityReached:
		return "capacity_reached"
	default:
		return "unknown"
	}
}

func (s *StreamDirector) Close() error {
	s.workers.DeleteAll()
	s.workers.Stop()
	s.workerAuth.Stop()
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
			s.workerAuth.Set(workerAuthToken, workerAuthInfo{
				requestId:         requestId,
				functionId:        apiFunc,
				functionVersionId: apiFuncVersion,
			}, ttlcache.DefaultTTL)
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
