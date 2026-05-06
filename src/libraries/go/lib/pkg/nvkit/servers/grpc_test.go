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
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	rt "runtime"
	"testing"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/auth"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/tracing"
)

func TestGRPCConfig_AddServerFlags(t *testing.T) {
	testGRPCConfig := &GRPCConfig{}
	// Check that passing nil command doesn't add any flags
	var nilCmd *cobra.Command
	ok := testGRPCConfig.AddServerFlags(nilCmd)
	assert.False(t, ok)

	// Check that there are flags added after the call
	nonNilCmd := &cobra.Command{}
	ok = testGRPCConfig.AddServerFlags(nonNilCmd)
	assert.True(t, ok)
	assert.True(t, nonNilCmd.Flags().HasFlags())
}

func TestNewGRPCServer(t *testing.T) {
	var tcDesc string

	// Test basic setup
	tcDesc = "Basic config"
	svr := NewGRPCServer(&GRPCConfig{})
	assert.NotNil(t, svr, tcDesc)
	sigTermCallbackFunc := func() {
		time.Sleep(10 * time.Second)
	}

	tracer, _ := tracing.SetupOTELTracer(&tracing.OTELConfig{Attributes: tracing.Attributes{
		ServiceName:    "test-service",
		ServiceVersion: "test-version",
	}})
	// Test basic setup with options
	tcDesc = "Basic config with options"
	svr = NewGRPCServer(&GRPCConfig{},
		WithLogger(log.NewNopLogger()),
		WithTracer(tracer),
		WithTerminationCallback(sigTermCallbackFunc),
	)
	assert.NotNil(t, svr, tcDesc)
	assert.Equal(t, svr.(*grpcServer).tracer, otel.GetTracerProvider(), tcDesc)
	assert.Equal(t, svr.(*grpcServer).logger, log.NewNopLogger())
	assert.Equal(t, rt.FuncForPC(reflect.ValueOf(svr.(*grpcServer).config.TerminationCallback).Pointer()).Name(), rt.FuncForPC(reflect.ValueOf(sigTermCallbackFunc).Pointer()).Name())
}

func TestGrpcServer_Setup(t *testing.T) {
	var tcDesc string
	// Test basic setup with options
	tcDesc = "Basic config with options"
	noopTracer := noop.NewTracerProvider()
	svr := NewGRPCServer(&GRPCConfig{BaseServerConfig: &BaseServerConfig{Tracing: tracing.OTELConfig{Enabled: false}}},
		WithLogger(log.NewNopLogger()),
		WithTracer(noopTracer),
	)
	// routerMuxx := http.DefaultServeMux
	// mux.NewRouter().
	// err := svr.Setup()
	// assert.Nil(t, err, tcDesc)
	assert.NotNil(t, svr, tcDesc)
	assert.Equal(t, noopTracer, otel.GetTracerProvider(), tcDesc)
	assert.Equal(t, svr.(*grpcServer).tracer, otel.GetTracerProvider(), tcDesc)
}

func TestExtraHTTPDialOption(t *testing.T) {
	var tcDesc string
	tcDesc = "New Server with Extra HTTP Dial Options"
	noopTracer := noop.NewTracerProvider()
	svr := NewGRPCServer(&GRPCConfig{
		BaseServerConfig: &BaseServerConfig{Tracing: tracing.OTELConfig{Enabled: false}}},
		WithLogger(log.NewNopLogger()),
		WithTracer(noopTracer),
		WithExtraHTTPDialOpts([]grpc.DialOption{
			grpc.WithDefaultCallOptions(
				grpc.MaxCallRecvMsgSize(100<<20),
				grpc.MaxCallSendMsgSize(100<<20),
			),
		}),
	)

	assert.NotNil(t, svr, tcDesc)
}
func TestAllGRPCServerOptions(t *testing.T) {
	noopTracer := noop.NewTracerProvider()

	// Test WithRegisterHandler
	handlerFunc := func(ctx context.Context, mux *runtime.ServeMux, conn *grpc.ClientConn) error {
		return nil
	}
	svr := NewGRPCServer(&GRPCConfig{BaseServerConfig: &BaseServerConfig{}},
		WithRegisterHandler(handlerFunc),
	)
	assert.NotNil(t, svr)
	assert.NotNil(t, svr.(*grpcServer).config.RegisterHandler)

	// Test WithRegisterServer
	serverFunc := func(s *grpc.Server) {}
	svr = NewGRPCServer(&GRPCConfig{BaseServerConfig: &BaseServerConfig{}},
		WithRegisterServer(serverFunc),
	)
	assert.NotNil(t, svr)
	assert.NotNil(t, svr.(*grpcServer).config.RegisterServer)

	// Test WithHTTPEndpointOverride
	httpOverrideFunc := func(mux *runtime.ServeMux) *runtime.ServeMux {
		return mux
	}
	svr = NewGRPCServer(&GRPCConfig{BaseServerConfig: &BaseServerConfig{}},
		WithHTTPEndpointOverride(httpOverrideFunc),
	)
	assert.NotNil(t, svr)
	assert.NotNil(t, svr.(*grpcServer).config.HTTPEndpointOverride)

	// Test WithPreServeCallback
	preServeFunc := func(s *grpc.Server) error {
		return nil
	}
	svr = NewGRPCServer(&GRPCConfig{BaseServerConfig: &BaseServerConfig{}},
		WithPreServeCallback(preServeFunc),
	)
	assert.NotNil(t, svr)
	assert.NotNil(t, svr.(*grpcServer).config.PreServeCallback)

	// Test WithExtraServerOpts
	serverOpts := []grpc.ServerOption{
		grpc.MaxConcurrentStreams(1000),
	}
	svr = NewGRPCServer(&GRPCConfig{BaseServerConfig: &BaseServerConfig{}},
		WithExtraServerOpts(serverOpts),
	)
	assert.NotNil(t, svr)
	assert.Len(t, svr.(*grpcServer).config.ExtraServerOpts, 1)

	// Test WithAdditionalHTTPHandlers
	handlers := map[string]http.Handler{
		"/test": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}),
	}
	svr = NewGRPCServer(&GRPCConfig{BaseServerConfig: &BaseServerConfig{}},
		WithAdditionalHTTPHandlers(handlers),
	)
	assert.NotNil(t, svr)
	assert.Len(t, svr.(*grpcServer).config.AdditionalHttpHandlers, 1)

	// Test WithAdditionalRequestHeaders
	headers := []string{"X-Custom-Header", "X-Another-Header"}
	svr = NewGRPCServer(&GRPCConfig{BaseServerConfig: &BaseServerConfig{}},
		WithAdditionalRequestHeaders(headers),
	)
	assert.NotNil(t, svr)
	assert.Len(t, svr.(*grpcServer).config.AdditionalHeaders, 2)

	// Test WithHttpHealthEndpoints
	healthEndpoints := []string{"/health", "/ready"}
	svr = NewGRPCServer(&GRPCConfig{BaseServerConfig: &BaseServerConfig{}},
		WithHttpHealthEndpoints(healthEndpoints...),
	)
	assert.NotNil(t, svr)
	assert.Len(t, svr.(*grpcServer).config.HttpHealthEndpointOverride, 2)

	// Test WithAdditionalServers
	serverPair := ServerFuncPair{
		Execute:   func() error { return nil },
		Interrupt: func(error) {},
	}
	svr = NewGRPCServer(&GRPCConfig{BaseServerConfig: &BaseServerConfig{}},
		WithAdditionalServers(serverPair),
	)
	assert.NotNil(t, svr)
	assert.Len(t, svr.(*grpcServer).config.AdditionalServers, 1)

	// Test WithCustomErrorHandler
	errorHandler := func(ctx context.Context, mux *runtime.ServeMux, marshaler runtime.Marshaler,
		w http.ResponseWriter, r *http.Request, err error) {
	}
	svr = NewGRPCServer(&GRPCConfig{BaseServerConfig: &BaseServerConfig{}},
		WithCustomErrorHandler(errorHandler),
	)
	assert.NotNil(t, svr)
	assert.NotNil(t, svr.(*grpcServer).config.CustomErrorHandler)

	// Test multiple options together
	svr = NewGRPCServer(&GRPCConfig{BaseServerConfig: &BaseServerConfig{Tracing: tracing.OTELConfig{Enabled: false}}},
		WithLogger(log.NewNopLogger()),
		WithTracer(noopTracer),
		WithRegisterHandler(handlerFunc),
		WithRegisterServer(serverFunc),
		WithAdditionalRequestHeaders(headers),
		WithHttpHealthEndpoints(healthEndpoints...),
	)
	assert.NotNil(t, svr)
	assert.Equal(t, noopTracer, svr.(*grpcServer).tracer)
}

func TestGRPCConfig_SetupTLSConfig(t *testing.T) {
	// Test with TLS disabled
	cfg := &GRPCConfig{
		BaseServerConfig: &BaseServerConfig{
			TLS: auth.TLSConfigOptions{
				Enabled: false,
			},
		},
	}
	cert, pool, err := cfg.SetupTLSConfig()
	assert.NoError(t, err)
	assert.Empty(t, cert.Certificate)
	assert.Nil(t, pool)

	// Test with invalid TLS config
	cfg = &GRPCConfig{
		BaseServerConfig: &BaseServerConfig{
			TLS: auth.TLSConfigOptions{
				Enabled:    true,
				CertFile:   "/nonexistent/cert.pem",
				KeyFile:    "/nonexistent/key.pem",
				RootCAFile: "/nonexistent/ca.pem",
			},
		},
	}
	cert, pool, err = cfg.SetupTLSConfig()
	assert.Error(t, err)
}

func TestGrpcServer_Setup_Success(t *testing.T) {
	noopTracer := noop.NewTracerProvider()

	// Test successful setup with all options
	svr := NewGRPCServer(&GRPCConfig{
		BaseServerConfig: &BaseServerConfig{
			Tracing: tracing.OTELConfig{Enabled: false},
		},
		GRPCAddr:  ":0",
		HTTPAddr:  ":0",
		AdminAddr: ":0",
	},
		WithLogger(log.NewNopLogger()),
		WithTracer(noopTracer),
	)

	err := svr.Setup()
	assert.NoError(t, err)
}

// mockHealthClient implements grpc_health_v1.HealthClient for testing.
type mockHealthClient struct {
	checkResp *grpc_health_v1.HealthCheckResponse
	checkErr  error
}

func (m *mockHealthClient) Check(_ context.Context, _ *grpc_health_v1.HealthCheckRequest, _ ...grpc.CallOption) (*grpc_health_v1.HealthCheckResponse, error) {
	return m.checkResp, m.checkErr
}

func (m *mockHealthClient) List(_ context.Context, _ *grpc_health_v1.HealthListRequest, _ ...grpc.CallOption) (*grpc_health_v1.HealthListResponse, error) {
	return nil, nil
}

func (m *mockHealthClient) Watch(_ context.Context, _ *grpc_health_v1.HealthCheckRequest, _ ...grpc.CallOption) (grpc_health_v1.Health_WatchClient, error) {
	return nil, nil
}

func TestGrpcServer_SetupHealthCheckHandler_Serving(t *testing.T) {
	svr := NewGRPCServer(&GRPCConfig{BaseServerConfig: &BaseServerConfig{}}).(*grpcServer)
	mc := &mockHealthClient{
		checkResp: &grpc_health_v1.HealthCheckResponse{Status: grpc_health_v1.HealthCheckResponse_SERVING},
	}
	handler := svr.setupHealthCheckHandler(mc)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/ping", nil)
	handler(w, r)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "OK", w.Body.String())
}

func TestGrpcServer_SetupHealthCheckHandler_NotServing(t *testing.T) {
	svr := NewGRPCServer(&GRPCConfig{BaseServerConfig: &BaseServerConfig{}}).(*grpcServer)
	mc := &mockHealthClient{
		checkResp: &grpc_health_v1.HealthCheckResponse{Status: grpc_health_v1.HealthCheckResponse_NOT_SERVING},
	}
	handler := svr.setupHealthCheckHandler(mc)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/ping", nil)
	handler(w, r)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestGrpcServer_SetupHealthCheckHandler_Error(t *testing.T) {
	svr := NewGRPCServer(&GRPCConfig{BaseServerConfig: &BaseServerConfig{}}).(*grpcServer)
	mc := &mockHealthClient{checkErr: fmt.Errorf("health check failed")}
	handler := svr.setupHealthCheckHandler(mc)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/ping", nil)
	handler(w, r)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestGrpcServer_ErrorHandler_WithHeaders(t *testing.T) {
	svr := NewGRPCServer(&GRPCConfig{BaseServerConfig: &BaseServerConfig{}}).(*grpcServer)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	// Set standard headers so the propagation path is exercised
	r.Header.Set("x-request-id", "req-111")
	r.Header.Set("x-nv-audit-id", "audit-222")
	r.Header.Set("etag", "v1")
	mux := runtime.NewServeMux()
	marshaler := &runtime.JSONPb{}
	svr.errorHandler(context.Background(), mux, marshaler, w, r, fmt.Errorf("test error"))
	assert.Equal(t, "req-111", w.Header().Get("x-request-id"))
	assert.Equal(t, "audit-222", w.Header().Get("x-nv-audit-id"))
	assert.Equal(t, "v1", w.Header().Get("etag"))
	assert.NotEqual(t, http.StatusOK, w.Code)
}

func TestGrpcServer_ErrorHandler_NoHeaders(t *testing.T) {
	svr := NewGRPCServer(&GRPCConfig{BaseServerConfig: &BaseServerConfig{}}).(*grpcServer)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	// No standard headers set – the empty-value branch should be hit
	mux := runtime.NewServeMux()
	marshaler := &runtime.JSONPb{}
	svr.errorHandler(context.Background(), mux, marshaler, w, r, fmt.Errorf("test error"))
	assert.Empty(t, w.Header().Get("x-request-id"))
	assert.NotEqual(t, http.StatusOK, w.Code)
}

func TestGrpcServer_Run_TLSError(t *testing.T) {
	svr := NewGRPCServer(&GRPCConfig{
		BaseServerConfig: &BaseServerConfig{
			TLS: auth.TLSConfigOptions{
				Enabled:  true,
				CertFile: "/nonexistent/cert.pem",
				KeyFile:  "/nonexistent/key.pem",
			},
		},
		GRPCAddr:  ":0",
		HTTPAddr:  ":0",
		AdminAddr: ":0",
	}, WithLogger(log.NewNopLogger()))
	err := svr.Run()
	assert.Error(t, err)
}

func TestGrpcServer_Run_AdminListenerError(t *testing.T) {
	// Occupy a port so that Run() fails when trying to bind AdminAddr.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	busyPort := ln.Addr().(*net.TCPAddr).Port

	svr := NewGRPCServer(&GRPCConfig{
		BaseServerConfig: &BaseServerConfig{},
		GRPCAddr:         ":0",
		HTTPAddr:         ":0",
		AdminAddr:        fmt.Sprintf("127.0.0.1:%d", busyPort),
	}, WithLogger(log.NewNopLogger()))
	err = svr.Run()
	assert.Error(t, err)
}

func TestGrpcServer_Run_GRPCListenerError(t *testing.T) {
	// Occupy a port so that Run() fails when trying to bind GRPCAddr.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	busyPort := ln.Addr().(*net.TCPAddr).Port

	svr := NewGRPCServer(&GRPCConfig{
		BaseServerConfig: &BaseServerConfig{},
		GRPCAddr:         fmt.Sprintf("127.0.0.1:%d", busyPort),
		HTTPAddr:         ":0",
		AdminAddr:        ":0",
	}, WithLogger(log.NewNopLogger()))
	err = svr.Run()
	assert.Error(t, err)
}

func TestGrpcServer_Run_HTTPListenerError(t *testing.T) {
	// Occupy a port so that Run() fails when trying to bind HTTPAddr.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	busyPort := ln.Addr().(*net.TCPAddr).Port

	svr := NewGRPCServer(&GRPCConfig{
		BaseServerConfig: &BaseServerConfig{},
		GRPCAddr:         ":0",
		HTTPAddr:         fmt.Sprintf("127.0.0.1:%d", busyPort),
		AdminAddr:        ":0",
	}, WithLogger(log.NewNopLogger()))
	err = svr.Run()
	assert.Error(t, err)
}
