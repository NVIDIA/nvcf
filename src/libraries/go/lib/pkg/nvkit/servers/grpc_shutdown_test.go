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

package servers

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/tracing"
)

func TestGrpcServer_RunTerminationCallback_Idempotent(t *testing.T) {
	t.Run("nil callback is ignored", func(t *testing.T) {
		svr := NewGRPCServer(&GRPCConfig{BaseServerConfig: &BaseServerConfig{}}, WithLogger(log.NewNopLogger())).(*grpcServer)

		svr.runTerminationCallback()

		assert.False(t, svr.terminationCallbackRan)
	})

	t.Run("callback only runs once", func(t *testing.T) {
		var calls atomic.Int32
		svr := NewGRPCServer(
			&GRPCConfig{BaseServerConfig: &BaseServerConfig{}},
			WithLogger(log.NewNopLogger()),
			WithTerminationCallback(func() {
				calls.Add(1)
			}),
		).(*grpcServer)

		svr.runTerminationCallback()
		svr.runTerminationCallback()

		assert.True(t, svr.terminationCallbackRan)
		assert.Equal(t, int32(1), calls.Load())
	})
}

func TestGrpcServer_ShutdownHTTPServer_Idempotent(t *testing.T) {
	t.Run("no server configured", func(t *testing.T) {
		svr := NewGRPCServer(&GRPCConfig{BaseServerConfig: &BaseServerConfig{}}, WithLogger(log.NewNopLogger())).(*grpcServer)

		shutDown, err := svr.shutdownHTTPServer(context.Background())

		require.NoError(t, err)
		assert.False(t, shutDown)
	})

	t.Run("active server only shuts down once", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)

		httpSrv := &http.Server{
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}),
		}
		serveErr := make(chan error, 1)
		go func() {
			serveErr <- httpSrv.Serve(ln)
		}()

		svr := NewGRPCServer(&GRPCConfig{BaseServerConfig: &BaseServerConfig{}}, WithLogger(log.NewNopLogger())).(*grpcServer)
		svr.setHTTPServer(httpSrv)

		shutDown, err := svr.shutdownHTTPServer(context.Background())
		require.NoError(t, err)
		assert.True(t, shutDown)
		assert.True(t, svr.httpServerShutdown)
		assert.ErrorIs(t, <-serveErr, http.ErrServerClosed)

		shutDown, err = svr.shutdownHTTPServer(context.Background())
		require.NoError(t, err)
		assert.True(t, shutDown)
	})
}

func TestGrpcServer_GracefulStopGRPCServer_Idempotent(t *testing.T) {
	t.Run("no server configured", func(t *testing.T) {
		svr := NewGRPCServer(&GRPCConfig{BaseServerConfig: &BaseServerConfig{}}, WithLogger(log.NewNopLogger())).(*grpcServer)

		svr.gracefulStopGRPCServer()

		assert.False(t, svr.grpcServerStopped)
	})

	t.Run("active server only stops once", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer ln.Close()

		grpcSrv := grpc.NewServer()
		serveErr := make(chan error, 1)
		go func() {
			serveErr <- grpcSrv.Serve(ln)
		}()

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		conn, err := grpc.DialContext(ctx, ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
		require.NoError(t, err)
		require.NoError(t, conn.Close())

		svr := NewGRPCServer(&GRPCConfig{BaseServerConfig: &BaseServerConfig{}}, WithLogger(log.NewNopLogger())).(*grpcServer)
		svr.setGRPCServer(grpcSrv)

		svr.gracefulStopGRPCServer()
		assert.True(t, svr.grpcServerStopped)
		require.NoError(t, <-serveErr)

		svr.gracefulStopGRPCServer()
	})
}

func TestGrpcServer_Setup_RegistersShutdownAndHealthHandlers(t *testing.T) {
	previousMux := http.DefaultServeMux
	http.DefaultServeMux = http.NewServeMux()
	defer func() {
		http.DefaultServeMux = previousMux
	}()

	healthListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer healthListener.Close()

	backendHealth := health.NewServer()
	backendHealth.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	backendGRPC := grpc.NewServer()
	grpc_health_v1.RegisterHealthServer(backendGRPC, backendHealth)
	backendErr := make(chan error, 1)
	go func() {
		backendErr <- backendGRPC.Serve(healthListener)
	}()
	defer func() {
		backendGRPC.GracefulStop()
		require.NoError(t, <-backendErr)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, healthListener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	require.NoError(t, err)
	require.NoError(t, conn.Close())

	var shutdownCalls atomic.Int32
	svr := NewGRPCServer(
		&GRPCConfig{
			BaseServerConfig: &BaseServerConfig{Tracing: tracing.OTELConfig{Enabled: false}},
			GRPCAddr:         healthListener.Addr().String(),
			HTTPAddr:         "127.0.0.1:0",
			AdminAddr:        "127.0.0.1:0",
		},
		WithLogger(log.NewNopLogger()),
		WithTracer(noop.NewTracerProvider()),
		WithTerminationCallback(func() {
			shutdownCalls.Add(1)
		}),
		WithHttpHealthEndpoints("/readyz"),
	).(*grpcServer)

	require.NoError(t, svr.Setup())
	require.NotNil(t, svr.healthServer)

	healthResp := httptest.NewRecorder()
	healthReq := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	http.DefaultServeMux.ServeHTTP(healthResp, healthReq)
	assert.Equal(t, http.StatusOK, healthResp.Code)
	assert.Equal(t, "OK", healthResp.Body.String())

	shutdownResp := httptest.NewRecorder()
	shutdownReq := httptest.NewRequest(http.MethodPost, defaultShutdownEndpoint, nil)
	http.DefaultServeMux.ServeHTTP(shutdownResp, shutdownReq)
	assert.Equal(t, http.StatusOK, shutdownResp.Code)
	assert.Equal(t, int32(1), shutdownCalls.Load())

	secondShutdownResp := httptest.NewRecorder()
	http.DefaultServeMux.ServeHTTP(secondShutdownResp, shutdownReq)
	assert.Equal(t, http.StatusOK, secondShutdownResp.Code)
	assert.Equal(t, int32(1), shutdownCalls.Load())
}

func TestGrpcServer_Setup_ReturnsTracerInitializationError(t *testing.T) {
	svr := NewGRPCServer(
		&GRPCConfig{
			BaseServerConfig: &BaseServerConfig{
				Tracing: tracing.OTELConfig{
					Enabled: true,
				},
			},
			GRPCAddr:  "127.0.0.1:0",
			HTTPAddr:  "127.0.0.1:0",
			AdminAddr: "127.0.0.1:0",
		},
		WithLogger(log.NewNopLogger()),
	).(*grpcServer)

	err := svr.Setup()

	require.Error(t, err)
	assert.Nil(t, svr.tracer)
}
