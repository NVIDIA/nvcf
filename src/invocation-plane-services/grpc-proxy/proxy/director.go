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

// pendingWorkInfo identifies a stateful work request this pod has issued a
// worker token for but has not yet seen a CONNECT for. The token lives only in
// this pod's memory, so if the pod goes away the queued request can never
// authenticate; the entry is what lets shutdown find it and drop it.
type pendingWorkInfo struct {
	functionVersionId string
}

// pendingWorkPurger removes a queued stateful work request. Implemented by the
// function invoker and asserted optionally, so an invoker that cannot reach the
// work queue (tests, alternative implementations) simply skips the purge.
type pendingWorkPurger interface {
	PurgePendingWork(ctx context.Context, requestId uuid.UUID, functionVersionId string) error
}

type StreamDirector struct {
	shuttingDown    *atomic.Bool
	workerAuth      *ttlcache.Cache[string, workerAuthInfo]  // auth -> request + function info
	issuedTokens    *ttlcache.Cache[string, issuedTokenInfo] // diagnostic only, see issuedTokenInfo
	pendingWork     *ttlcache.Cache[uuid.UUID, pendingWorkInfo]
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

	// Sessions waiting on a worker CONNECT. Retention is deliberately much
	// longer than the token TTL: the point is to still know about a request
	// whose token has already aged out, because that request is still sitting
	// in the work queue. Bounded so it cannot grow without limit.
	pendingWorkCache := ttlcache.New(
		// Deliberately reuses the issued-token retention and capacity rather
		// than introducing its own. This tracks the same population from the
		// same call site, and the codebase does not need another timeout.
		ttlcache.WithTTL[uuid.UUID, pendingWorkInfo](issuedTokenRetention),
		ttlcache.WithCapacity[uuid.UUID, pendingWorkInfo](issuedTokenCacheCapacity),
		ttlcache.WithDisableTouchOnHit[uuid.UUID, pendingWorkInfo](),
	)
	go pendingWorkCache.Start()

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
		closedAt := wc.ClosedAt()
		if closedAt.IsZero() {
			// No transport-level close was stamped, so this eviction is the
			// first thing that noticed. Attribute it to now rather than
			// leaving the field empty.
			closedAt = time.Now()
		}
		// held_for is now measured to the close itself rather than to whenever
		// this callback ran. The callback can lag the close, so the old value
		// was the tunnel's lifetime plus an unknown amount of cache latency,
		// which is precisely the error that makes cross-component correlation
		// hard. Where no close was stamped the fallback above reproduces the
		// previous behaviour exactly.
		heldFor := closedAt.Sub(wc.CreatedAt)
		closeInfo := worker.ClassifyCloseError(wc.CloseError())

		metrics.WorkerConnectionClosedTotal.WithLabelValues(reasonStr).Inc()
		metrics.WorkerConnectionCloseCodeTotal.WithLabelValues(closeInfo.Code).Inc()
		metrics.WorkerConnectionsActive.Dec()
		metrics.WorkerConnectionDurationSeconds.Observe(heldFor.Seconds())

		// Promoted from debug to info: this is the only place the proxy records
		// WHY it dropped a worker tunnel, and that question is routinely asked
		// during incidents. At debug it was unavailable in production exactly
		// when it was needed. The message text is unchanged so existing log
		// searches keep working.
		logFields := []zap.Field{
			zap.Stringer("request_id", i.Key().requestId),
			zap.String("eviction_reason", reasonStr),
			zap.String("raw_eviction_reason", mapEvictionReason(reason)),
			zap.Duration("held_for", heldFor),
			// Explicit timestamps: the eviction callback can run measurably
			// after the transport went away, so the log line's own timestamp
			// cannot be used to correlate against other components.
			zap.Time("opened_at", wc.CreatedAt),
			zap.Time("closed_at", closedAt),
			// What the transport reported, as opposed to which side tore down.
			zap.String("close_code", closeInfo.Code),
			zap.String("local_timeout", localTimeoutFor(reasonStr, closeInfo.Code)),
		}
		if closeInfo.Detail != "" {
			logFields = append(logFields, zap.String("close_detail", closeInfo.Detail))
		}
		if closeInfo.Remote != nil {
			// Only QUIC tells us this. Absence means unknown, not local.
			logFields = append(logFields, zap.Bool("closed_by_peer", *closeInfo.Remote))
		}
		logFields = append(logFields,
			zap.String("function_id", wc.FunctionId),
			zap.String("function_version_id", wc.FunctionVersionId))

		zap.L().Info("worker connection cache eviction triggered", logFields...)

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
				attribute.String("close_code", closeInfo.Code),
				attribute.String("close_detail", closeInfo.Detail),
				attribute.String("local_timeout", localTimeoutFor(reasonStr, closeInfo.Code)),
				attribute.String("closed_at", closedAt.Format(time.RFC3339Nano)),
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
		pendingWork:     pendingWorkCache,
		workerAuth:      workerAuthCache,
		functionInvoker: functionInvoker,
		cors:            cors.New(middleware.DefaultCorsOptions),
	}
}

// Timer names reported in local_timeout. Only the proxy's own timers can be
// named here.
const (
	localTimeoutNone           = ""
	localTimeoutWorkerCacheTTL = "worker_cache_ttl"
	localTimeoutTransportIdle  = "transport_idle"
	localTimeoutQUICIdle       = "quic_idle"
)

// localTimeoutFor names which of the proxy's own timers fired, where one did.
//
// Deliberately conservative. Timers owned by other components on the path
// cannot be identified from here, and guessing at them would be worse than
// saying nothing: close_code still characterises those cases. An empty result
// means "not one of ours", not "no timer".
func localTimeoutFor(evictionReason, closeCode string) string {
	if evictionReason == metrics.CloseReasonTTLExpired {
		// consts.Timeout on the worker connection cache.
		return localTimeoutWorkerCacheTTL
	}
	switch closeCode {
	case worker.CloseCodeQUICIdleTimeout:
		// quic-go's MaxIdleTimeout. Either endpoint can own this, so it is
		// named but not attributed.
		return localTimeoutQUICIdle
	case worker.CloseCodeTimeout:
		// The HTTP/1 and HTTP/2 transports are configured with consts.Timeout
		// for idle, read and write. A bare net timeout on this path is one of
		// those.
		return localTimeoutTransportIdle
	}
	return localTimeoutNone
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

// purgePendingWork drops the work requests for sessions that never got a
// worker CONNECT.
//
// Best effort by design. A session records its pending work just before the
// invocation publishes the work request, so a shutdown landing precisely
// between those two steps will miss that one request. Closing that window
// needs an admission gate and a drain timeout, which is more machinery and
// another tunable than the gap justifies: a missed request is simply left as
// it is today, and today every one of them is left.
//
// Their tokens exist only in this pod's memory, so once it is
// gone every one of them is guaranteed to be rejected; leaving them queued
// means each is still pulled, still takes a concurrency slot, and still fails.
//
// Sessions with a worker already attached are deliberately not touched. Those
// can reattach through another pod, and their work request has already left the
// queue anyway.
// purgeDepartedClientWork drops the queued work for a request whose client
// stopped waiting before any worker attached.
//
// This is the trigger the evidence points at. A worker auth token is valid for
// consts.Timeout, the same budget after which the client is sent a gateway
// timeout, so the two are matched by design. Under load the work sits in the
// queue well past that: measured token age at CONNECT ran to 30-60s against a
// 30s budget. Every one of those is fetched, occupies a worker slot, attempts
// a CONNECT and is correctly rejected, for a client that has already given up.
//
// Left queued they are still delivered, and it is that delivery, not the
// rejection, that keeps a saturated function at zero goodput. Dropping them
// here removes work that provably cannot be served while leaving every live
// request untouched, since a session that reaches this point has no client.
func (s *StreamDirector) purgeDepartedClientWork(requestId uuid.UUID, functionVersionId string) {
	purger, ok := s.functionInvoker.(pendingWorkPurger)
	if !ok {
		metrics.PendingWorkPurgeSkippedTotal.WithLabelValues(metrics.PurgeSkipUnsupported).Inc()
		return
	}
	s.pendingWork.Delete(requestId)

	ctx, cancel := context.WithTimeout(context.Background(), consts.Timeout)
	defer cancel()

	if err := purger.PurgePendingWork(ctx, requestId, functionVersionId); err != nil {
		metrics.PendingWorkPurgedTotal.WithLabelValues(metrics.PurgeFailed, metrics.PurgeTriggerClientDeparted).Inc()
		// Warning rather than error: the request is already lost to its
		// client either way, and the purge is what stops a worker spending a
		// slot on it later.
		zap.L().Warn("failed to purge work for a client that stopped waiting",
			zap.Stringer("request_id", requestId),
			zap.String("function_version_id", functionVersionId),
			zap.Error(err))
		return
	}
	metrics.PendingWorkPurgedTotal.WithLabelValues(metrics.PurgeSucceeded, metrics.PurgeTriggerClientDeparted).Inc()
	zap.L().Debug("purged queued work for a client that stopped waiting",
		zap.Stringer("request_id", requestId))
}

func (s *StreamDirector) purgePendingWork() {
	purger, ok := s.functionInvoker.(pendingWorkPurger)
	if !ok {
		// Silent here would be indistinguishable from a clean shutdown that
		// had nothing queued, which is the reading that made the last stage
		// deployment of this change look like a no-op with no way to tell.
		metrics.PendingWorkPurgeSkippedTotal.WithLabelValues(metrics.PurgeSkipUnsupported).Inc()
		zap.L().Info("pending work purge skipped, invoker cannot reach the work queue")
		return
	}
	pending := s.pendingWork.Items()
	if len(pending) == 0 {
		metrics.PendingWorkPurgeSkippedTotal.WithLabelValues(metrics.PurgeSkipNothing).Inc()
		zap.L().Info("pending work purge found nothing queued")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), consts.Timeout)
	defer cancel()

	var purged, failed int
	for requestId, item := range pending {
		if ctx.Err() != nil {
			// Out of budget. Report what is left rather than trailing off
			// silently, so a shutdown that could not finish is visible.
			failed += len(pending) - purged - failed
			break
		}
		if err := purger.PurgePendingWork(ctx, requestId, item.Value().functionVersionId); err != nil {
			failed++
			// Expected when this service has no rights on the work queue, so
			// this stays a warning: the purge is an optimisation and shutdown
			// is still correct without it.
			zap.L().Warn("failed to purge pending stateful work request on shutdown",
				zap.Stringer("request_id", requestId),
				zap.String("function_version_id", item.Value().functionVersionId),
				zap.Error(err))
			continue
		}
		purged++
	}

	metrics.PendingWorkPurgedTotal.WithLabelValues(metrics.PurgeSucceeded, metrics.PurgeTriggerShutdown).Add(float64(purged))
	metrics.PendingWorkPurgedTotal.WithLabelValues(metrics.PurgeFailed, metrics.PurgeTriggerShutdown).Add(float64(failed))
	zap.L().Info("purged pending stateful work requests on shutdown",
		zap.Int("purged", purged),
		zap.Int("failed", failed))
}

func (s *StreamDirector) Close() error {
	// Mark first: DeleteAll evicts every entry, and without this those
	// evictions would be misreported as client or worker initiated.
	s.shuttingDown.Store(true)
	// Before the caches go away, drop the queued work this pod can no longer
	// authenticate. Best effort: a failure here must not hold up shutdown.
	s.purgePendingWork()
	s.workers.DeleteAll()
	s.workers.Stop()
	s.workerAuth.Stop()
	s.issuedTokens.Stop()
	s.pendingWork.Stop()
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
			// Remembered until the worker CONNECTs back, so that a shutdown can
			// find the requests whose tokens are about to be lost with this pod
			// and drop them from the work queue instead of leaving them to be
			// pulled and rejected.
			s.pendingWork.Set(requestId, pendingWorkInfo{functionVersionId: apiFuncVersion}, ttlcache.DefaultTTL)
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
				_, connected := workerConnection.WaitForConnection(ctx)
				cancelInvokingWorker()
				if !connected {
					s.purgeDepartedClientWork(invokeResponse.RequestId, apiFunctionVersionId)
				}
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
