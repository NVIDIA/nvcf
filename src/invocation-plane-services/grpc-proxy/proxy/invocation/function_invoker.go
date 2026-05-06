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
package invocation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"time"

	nverrors "github.com/NVIDIA/nvcf-go/pkg/nvkit/errors"
	"github.com/google/uuid"
	grpcretry "github.com/grpc-ecosystem/go-grpc-middleware/retry"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/samber/lo"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	grpcCodes "google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"nvcf-grpc-proxy/nvcf/pb"
	"nvcf-grpc-proxy/proxy/metrics"
)

var otelFieldCount = len(otel.GetTextMapPropagator().Fields())
var ErrSessionNotFound = nverrors.NewNVError(errors.New("no existing session found")).WithCode(grpcCodes.NotFound)

type geoLookuper interface {
	LookupRegions(ctx context.Context, clientIP net.IP) []string
}

type rateLimiter interface {
	IsRateLimited(ctx context.Context, ncaId, functionId, functionVersionId string, isSyncCheck bool) bool
}

type FunctionInvoker struct {
	nc                  *nats.Conn
	js                  jetstream.JetStream
	nvcfClient          pb.ProxyClient
	connectPaths        ConnectPaths
	region              string
	requestRegistration *StatefulRequestRegistration
	geoLookup           geoLookuper
	rateLimit           rateLimiter
}

func NewFunctionInvoker(nc *nats.Conn, nvcfClient pb.ProxyClient, connectPaths ConnectPaths, jetstreamPlacementTag, region string, geoLookup geoLookuper, rateLimit rateLimiter) (*FunctionInvoker, error) {
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, err
	}
	requestRegistration, err := NewStatefulRequestRegistration(context.Background(), js, jetstreamPlacementTag, region)
	if err != nil {
		return nil, err
	}

	return &FunctionInvoker{
		nc:                  nc,
		js:                  js,
		nvcfClient:          nvcfClient,
		connectPaths:        connectPaths,
		region:              region,
		requestRegistration: requestRegistration,
		geoLookup:           geoLookup,
		rateLimit:           rateLimit,
	}, nil
}

func (f *FunctionInvoker) Close() error {
	f.js.CleanupPublisher()
	if closer, ok := f.rateLimit.(io.Closer); ok {
		_ = closer.Close()
	}
	return nil
}

// Result is a named tuple for clearer identities of the return values. intended to be passed by value.
type Result struct {
	WorkerAuthorizationToken string
	RequestId                uuid.UUID
}

func (f *FunctionInvoker) InvokeStatefulFunction(ctx context.Context, conn net.Conn, clientAuth, functionId, functionVersionId string, existingRequestId *uuid.UUID, onWorkerAuthSet func(workerAuthToken string, requestId uuid.UUID, apiFunctionId string, apiFunctionVersionId string)) (Result, context.CancelFunc, error) {
	ctx, span := otel.GetTracerProvider().Tracer("proxy-tracer").Start(ctx, "InvokeStatefulFunction",
		trace.WithAttributes(
			attribute.String("function_id", functionId),
			attribute.String("function_version_id", functionVersionId),
			attribute.Bool("has_existing_request_id", existingRequestId != nil),
		))
	defer span.End()

	startInitTime := time.Now()

	proxyAuthResponse, err := f.nvcfClient.AuthStatefulWork(ctx, &pb.ProxyAuthRequest{
		ClientAuthorizationToken: clientAuth,
		FunctionId:               functionId,
		FunctionVersionId:        emptyAsNil(functionVersionId),
	}, grpcretry.WithMax(3))
	if err != nil {
		return Result{}, nil, err
	}

	// the worker needs to send this token back along with the matching request id to get paired up with the client connection
	workerAuthToken := generateWorkerToken()
	var requestId uuid.UUID
	if existingRequestId == nil {
		requestId = uuid.New()
		span.SetAttributes(attribute.Stringer("request_id", requestId))
		functionVersion, err := pickFunctionVersion(proxyAuthResponse)
		if err != nil {
			return Result{}, nil, err
		}

		metrics.IncrFunctionRequest(proxyAuthResponse.FunctionId, functionVersion.FunctionVersionId, proxyAuthResponse.ClientNcaId)

		if functionVersion.GetHasRateLimit() && f.rateLimit.IsRateLimited(ctx, proxyAuthResponse.ClientNcaId, proxyAuthResponse.FunctionId, functionVersion.FunctionVersionId, functionVersion.GetSyncCheck()) {
			// maps to 429 if non-grpc
			err := nverrors.NewNVError(errors.New("exceeded rate limit")).WithCode(grpcCodes.ResourceExhausted)
			return Result{}, nil, err
		}

		onWorkerAuthSet(workerAuthToken, requestId, proxyAuthResponse.FunctionId, functionVersion.FunctionVersionId)

		cancelInvokingWorker, err := f.startNewSession(ctx, conn, requestId, proxyAuthResponse, workerAuthToken, functionVersion)
		if err != nil {
			return Result{}, nil, err
		}
		elapsed := time.Since(startInitTime).Seconds()
		metrics.SessionInitTimeCounter.WithLabelValues("false").Observe(elapsed)
		return Result{
			WorkerAuthorizationToken: workerAuthToken,
			RequestId:                requestId,
		}, cancelInvokingWorker, nil
	} else {
		// if there is an existing request id, check that it matches up with an existing request
		// must match function id, version id if provided, and nca id of the client
		requestId = *existingRequestId
		span.SetAttributes(attribute.Stringer("request_id", requestId))
		existingSession, err := f.requestRegistration.LookupStatefulSession(ctx, requestId)
		if err != nil {
			if errors.Is(err, jetstream.ErrMsgNotFound) {
				return Result{}, nil, fmt.Errorf("%w for request id %s", ErrSessionNotFound, requestId)
			}
			return Result{}, nil, err
		}
		// purposely omitting the mismatched data to not leak session information
		if existingSession.FunctionId != functionId {
			return Result{}, nil, fmt.Errorf("existing session function ID mismatch for request id %s", requestId)
		}
		if functionVersionId != "" && existingSession.FunctionVersionId != functionVersionId {
			return Result{}, nil, fmt.Errorf("existing session function version ID mismatch for request id %s", requestId)
		}
		if proxyAuthResponse.ClientNcaId != existingSession.InvokerNcaId {
			return Result{}, nil, fmt.Errorf("existing session invoker NCA ID mismatch for request id %s", requestId)
		}

		onWorkerAuthSet(workerAuthToken, requestId, existingSession.FunctionId, existingSession.FunctionVersionId)

		err = f.joinExistingSession(ctx, requestId, proxyAuthResponse, workerAuthToken)
		if err != nil {
			return Result{}, nil, err
		}
		elapsed := time.Since(startInitTime).Seconds()
		metrics.SessionInitTimeCounter.WithLabelValues("true").Observe(elapsed)
		return Result{
			WorkerAuthorizationToken: workerAuthToken,
			RequestId:                requestId,
		}, nil, nil
	}
}

func generateWorkerToken() string {
	return lo.RandomString(64, lo.AlphanumericCharset)
}

func pickFunctionVersion(proxyAuthResponse *pb.ProxyAuthResponse) (*pb.ProxyAuthResponse_FunctionVersion, error) {
	if len(proxyAuthResponse.FunctionVersions) <= 0 {
		return nil, fmt.Errorf("missing function versions from nvcf api response")
	}
	return proxyAuthResponse.FunctionVersions[rand.IntN(len(proxyAuthResponse.FunctionVersions))], nil
}

func (f *FunctionInvoker) startNewSession(ctx context.Context, conn net.Conn, requestId uuid.UUID, proxyAuthResponse *pb.ProxyAuthResponse, workerAuthToken string, functionVersion *pb.ProxyAuthResponse_FunctionVersion) (context.CancelFunc, error) {
	marshalledInvokeFunctionRequest, err := f.marshalStatefulSessionRequest(requestId, proxyAuthResponse, workerAuthToken)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	if functionVersion.GetType() == pb.ProxyAuthResponse_FunctionVersion_STREAMING {
		go f.sendLLSInvocationRequest(ctx, conn, requestId, functionVersion, marshalledInvokeFunctionRequest)
	} else {
		subject := f.requestStreamSubject(functionVersion.FunctionVersionId, requestId)
		_, err = f.js.PublishMsg(ctx, &nats.Msg{
			Subject: subject,
			Header:  otelHeaders(ctx),
			Data:    marshalledInvokeFunctionRequest,
		})
		if err != nil {
			cancel()
			return nil, fmt.Errorf("failed to publish function invocation request to nats: %w", err)
		}
	}
	return cancel, nil
}

func (f *FunctionInvoker) sendLLSInvocationRequest(ctx context.Context, conn net.Conn, requestId uuid.UUID, functionVersion *pb.ProxyAuthResponse_FunctionVersion, marshalledInvokeFunctionRequest []byte) {
	ctx, span := otel.GetTracerProvider().Tracer("proxy-tracer").Start(ctx, "sendLLSInvocationRequest",
		trace.WithAttributes(
			attribute.Stringer("request_id", requestId),
		))
	defer span.End()
	var regions []string
	// check for GFN and non-GFN. only GFN is valid for geo lookup.
	if functionVersion.GetBackendType() != pb.ProxyAuthResponse_FunctionVersion_GFN {
		regions = []string{"default"}
	} else {
		regions = append(f.geoLookup.LookupRegions(ctx, getRemoteIP(conn)), "default")
		span.SetAttributes(
			attribute.StringSlice("lls_routing_regions", regions),
			attribute.String("client_ip", conn.RemoteAddr().String()),
		)
	}
	for i := 0; i < len(regions) && ctx.Err() == nil; {
		err := f.tryRegionForLLS(ctx, regions[i], requestId, functionVersion, marshalledInvokeFunctionRequest)
		if err == nil {
			return
		}
		// short circuit and try the next region
		if !errors.Is(err, nats.ErrNoResponders) &&
			!errors.Is(err, context.Canceled) &&
			!errors.Is(err, context.DeadlineExceeded) &&
			ctx.Err() == nil { // a sub-operation may have timed out, but not our root context
			zap.L().Warn("failed to send lls message", zap.Error(err))
			SleepWithContext(ctx, 3*time.Second)
		}
		// stay on the last region until ctx runs out. it will be the default region.
		if i != len(regions)-1 {
			i++
		} else {
			// rate limit if we've already tried all the regions and are on the default region
			SleepWithContext(ctx, time.Second)
		}
	}
}

func getRemoteIP(conn net.Conn) net.IP {
	// String() for tcp connections returns the port too so we cast to directly get the IP
	if addr, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		return addr.IP
	}
	// best effort
	return net.ParseIP(conn.RemoteAddr().String())
}

func (f *FunctionInvoker) tryRegionForLLS(ctx context.Context, region string, requestId uuid.UUID, functionVersion *pb.ProxyAuthResponse_FunctionVersion, marshalledInvokeFunctionRequest []byte) error {
	ctx, span := otel.GetTracerProvider().Tracer("proxy-tracer").Start(ctx, "tryRegionForLLS",
		trace.WithAttributes(
			attribute.String("region", region),
		))
	defer span.End()

	// keep trying to get a worker until either somebody responds and we get cancelled or we
	// get cancelled for some other reason
	if ctx.Err() != nil {
		return ctx.Err()
	}

	// dropped message protection:
	// the root ctx will get cancelled when the client disconnects, but we want a shorter ctx here
	// to account for multiple retries within one client request. a nats request may have been
	// received by a worker but then dropped which would cause a non-shortened-timeout request to
	// continue waiting for a reply until the client eventually leaves rather than retrying.
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	subject := llsRequestSubject(region, functionVersion.FunctionVersionId, requestId)
	_, err := f.nc.RequestMsgWithContext(ctx, &nats.Msg{
		Subject: subject,
		Header:  otelHeaders(ctx),
		Data:    marshalledInvokeFunctionRequest,
	})

	return err
}

func (f *FunctionInvoker) joinExistingSession(ctx context.Context, requestId uuid.UUID, proxyAuthResponse *pb.ProxyAuthResponse, workerAuthToken string) error {
	marshalledInvokeFunctionRequest, err := f.marshalStatefulSessionRequest(requestId, proxyAuthResponse, workerAuthToken)
	if err != nil {
		return err
	}
	subject := reconnectSubject(requestId)
	err = f.nc.PublishMsg(&nats.Msg{
		Subject: subject,
		Header:  otelHeaders(ctx),
		Data:    marshalledInvokeFunctionRequest,
	})
	if err != nil {
		return fmt.Errorf("failed to publish function invocation request to nats: %w", err)
	}
	return nil
}

func otelHeaders(ctx context.Context) nats.Header {
	carrier := make(NatsHeaderCarrier, otelFieldCount)
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	return nats.Header(carrier)
}

func (f *FunctionInvoker) marshalStatefulSessionRequest(requestId uuid.UUID, proxyAuthResponse *pb.ProxyAuthResponse, workerAuthToken string) ([]byte, error) {
	connectionConfigs := make([]*pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig, 0, 2)
	if f.connectPaths.HTTP1 != "" {
		connectionConfigs = append(connectionConfigs, &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig{
			Config: &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_Http1Config{Http1Config: &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_HTTP1ConnectionConfig{
				ProxyURI:                f.connectPaths.HTTP1,
				ProxyAuthorizationToken: workerAuthToken,
			}},
		})
	}
	if f.connectPaths.HTTP3 != "" {
		connectionConfigs = append(connectionConfigs, &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig{
			Config: &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_Http3Config{Http3Config: &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_HTTP3ConnectionConfig{
				ProxyURI:                f.connectPaths.HTTP3,
				ProxyAuthorizationToken: workerAuthToken,
			}},
		})
	}
	invokeFunctionRequest := pb.WorkerInvokeFunctionRequest{
		RequestId: requestId.String(),
		NcaId:     proxyAuthResponse.ClientNcaId,
		Subject:   proxyAuthResponse.ClientAuthSubject,
		StatefulConfig: &pb.WorkerInvokeFunctionRequest_StatefulConfig{
			NvcfRegionOfInvoker: f.region,
			ConnectionConfigs:   connectionConfigs,
		},
		RequestTime: timestamppb.Now(),
	}
	marshalledInvokeFunctionRequest, err := proto.Marshal(&invokeFunctionRequest)
	if err != nil {
		return nil, err
	}
	return marshalledInvokeFunctionRequest, nil
}

// format of a subject is rq.${region}.${function_version}.${request_id}
func (f *FunctionInvoker) requestStreamSubject(functionVersion string, requestId uuid.UUID) string {
	return fmt.Sprintf("rq.%s.%s.%s", f.region, functionVersion, requestId)
}

// format of a subject is stateful_session.reconnect.${request_id}
func reconnectSubject(requestId uuid.UUID) string {
	return fmt.Sprintf("stateful_session.reconnect.%s", requestId)
}

// format of a subject is llsrq.${region}.${function_version}.${request_id}
func llsRequestSubject(region, functionVersion string, requestId uuid.UUID) string {
	return fmt.Sprintf("llsrq.%s.%s.%s", region, functionVersion, requestId)
}

func emptyAsNil(s string) *string {
	var ptr *string
	if s != "" {
		ptr = &s
	}
	return ptr
}

type ConnectPaths struct {
	HTTP1 string
	HTTP3 string
}
