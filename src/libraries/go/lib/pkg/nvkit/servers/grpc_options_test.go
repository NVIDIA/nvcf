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
	"net/http"
	"testing"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
)

func newTestCfg() *GRPCConfig {
	return &GRPCConfig{BaseServerConfig: &BaseServerConfig{}}
}

func TestWithRegisterHandler(t *testing.T) {
	cfg := newTestCfg()
	fn := RegisterHandlerFunc(func(_ context.Context, _ *runtime.ServeMux, _ *grpc.ClientConn) error {
		return nil
	})
	NewGRPCServer(cfg, WithRegisterHandler(fn))
	assert.NotNil(t, cfg.RegisterHandler)
}

func TestWithRegisterServer(t *testing.T) {
	cfg := newTestCfg()
	fn := RegisterServerFunc(func(_ *grpc.Server) {})
	NewGRPCServer(cfg, WithRegisterServer(fn))
	assert.NotNil(t, cfg.RegisterServer)
}

func TestWithHTTPEndpointOverride(t *testing.T) {
	cfg := newTestCfg()
	fn := HTTPEndpointOverrideFunc(func(mux *runtime.ServeMux) *runtime.ServeMux { return mux })
	NewGRPCServer(cfg, WithHTTPEndpointOverride(fn))
	assert.NotNil(t, cfg.HTTPEndpointOverride)
}

func TestWithPreServeCallback(t *testing.T) {
	cfg := newTestCfg()
	fn := PreServeCallbackFunc(func(_ *grpc.Server) error { return nil })
	NewGRPCServer(cfg, WithPreServeCallback(fn))
	assert.NotNil(t, cfg.PreServeCallback)
}

func TestWithExtraServerOpts(t *testing.T) {
	cfg := newTestCfg()
	opts := []grpc.ServerOption{grpc.MaxConcurrentStreams(100)}
	NewGRPCServer(cfg, WithExtraServerOpts(opts))
	assert.Len(t, cfg.ExtraServerOpts, 1)
}

func TestWithAdditionalHTTPHandlers(t *testing.T) {
	cfg := newTestCfg()
	handlers := map[string]http.Handler{
		"/test": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}),
	}
	NewGRPCServer(cfg, WithAdditionalHTTPHandlers(handlers))
	assert.NotNil(t, cfg.AdditionalHttpHandlers)
	assert.Len(t, cfg.AdditionalHttpHandlers, 1)
}

func TestWithAdditionalRequestHeaders(t *testing.T) {
	cfg := newTestCfg()
	headers := []string{"X-Request-ID", "X-Audit-ID"}
	NewGRPCServer(cfg, WithAdditionalRequestHeaders(headers))
	assert.Equal(t, headers, cfg.AdditionalHeaders)
}

func TestWithHttpHealthEndpoints(t *testing.T) {
	cfg := newTestCfg()
	NewGRPCServer(cfg, WithHttpHealthEndpoints("/healthz", "/readyz"))
	assert.Len(t, cfg.HttpHealthEndpointOverride, 2)
	assert.Contains(t, cfg.HttpHealthEndpointOverride, "/healthz")
}

func TestWithAdditionalServers(t *testing.T) {
	cfg := newTestCfg()
	pair := ServerFuncPair{}
	NewGRPCServer(cfg, WithAdditionalServers(pair))
	assert.Len(t, cfg.AdditionalServers, 1)
}

func TestWithCustomErrorHandler(t *testing.T) {
	cfg := newTestCfg()
	fn := CustomErrorHandlerFunc(func(_ context.Context, _ *runtime.ServeMux, _ runtime.Marshaler, _ http.ResponseWriter, _ *http.Request, _ error) {
	})
	NewGRPCServer(cfg, WithCustomErrorHandler(fn))
	assert.NotNil(t, cfg.CustomErrorHandler)
}

func TestWithExtraHTTPDialOptsOption(t *testing.T) {
	cfg := newTestCfg()
	opts := []grpc.DialOption{grpc.WithBlock()}
	NewGRPCServer(cfg, WithExtraHTTPDialOpts(opts))
	assert.Len(t, cfg.ExtraHTTPDialOpts, 1)
}

func TestSetAdminRoutes_WithCustomEndpoint(t *testing.T) {
	cfg := &BaseServerConfig{ShutdownEndpoint: "/custom-shutdown"}
	mux := http.NewServeMux()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	cfg.SetAdminRoutes(mux, handler)
	assert.Equal(t, "/custom-shutdown", cfg.ShutdownEndpoint)
}

func TestSetAdminRoutes_DefaultEndpoint(t *testing.T) {
	cfg := &BaseServerConfig{}
	mux := http.NewServeMux()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	cfg.SetAdminRoutes(mux, handler)
	assert.Equal(t, defaultShutdownEndpoint, cfg.ShutdownEndpoint)
}

func TestStandardTracer_TracingDisabled(t *testing.T) {
	cfg := &BaseServerConfig{ServiceName: "test-svc"}
	tracer, err := StandardTracer(cfg)
	assert.NoError(t, err)
	assert.NotNil(t, tracer)
}
