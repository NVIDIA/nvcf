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

//Code was lifted from otel repo.

package testutils

import (
	"context"
	"net"
	"sync"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/logs"
	"go.uber.org/zap"
	"google.golang.org/grpc"

	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
)

func makeMockCollector() *MockCollector {
	return &MockCollector{
		traceSvc: &mockTraceService{},
		stopped:  make(chan struct{}),
	}
}

type mockTraceService struct {
	collectortracepb.UnimplementedTraceServiceServer

	partial *collectortracepb.ExportTracePartialSuccess
	mu      sync.RWMutex
}

func (mts *mockTraceService) Export(ctx context.Context, exp *collectortracepb.ExportTraceServiceRequest) (*collectortracepb.ExportTraceServiceResponse, error) {
	mts.mu.Lock()
	defer mts.mu.Unlock()

	reply := &collectortracepb.ExportTraceServiceResponse{
		PartialSuccess: mts.partial,
	}
	return reply, nil
}

type MockCollector struct {
	traceSvc *mockTraceService

	endpoint string
	stopFunc func()
	stopped  chan struct{}
}

var _ collectortracepb.TraceServiceServer = (*mockTraceService)(nil)

func (mc *MockCollector) Shutdown() {
	zap.L().Info("OTEL shutting down")
	mc.stopFunc()
	zap.L().Info("OTEL has shut down gracefully")
}

func NewMockCollector(zapLogger *logs.ZapLogger) (*MockCollector, error) {
	addr := "127.0.0.1:8360"
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}

	srv := grpc.NewServer()
	mc := makeMockCollector()
	collectortracepb.RegisterTraceServiceServer(srv, mc.traceSvc)
	go func() {
		_ = srv.Serve(ln)
		close(mc.stopped)
	}()

	zap.L().Info("Starting OTEL", zap.String("listen", addr))
	mc.endpoint = ln.Addr().String()
	mc.stopFunc = srv.Stop

	return mc, nil
}
