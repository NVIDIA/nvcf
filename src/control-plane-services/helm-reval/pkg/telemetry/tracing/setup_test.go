// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tracing_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/reval/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/telemetry/tracing"
)

// ApplyTracing exercises newExporter + newTraceProvider in one shot.
// Uses Insecure=true and an unreachable localhost endpoint; the OTLP gRPC
// client connects lazily so construction itself does not require a server.
func TestApplyTracing_InsecureLifecycle(t *testing.T) {
	logger := zap.NewNop()
	tcfg := &config.TracingConfig{
		Enabled:  true,
		Endpoint: "127.0.0.1:0",
		Insecure: true,
	}
	telcfg := &config.TelemetryConfig{
		ServiceName:               "reval-test",
		ServiceVersion:            "0.0.0-test",
		DeploymentEnvironmentName: "unit",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	deferFunc, tp := tracing.ApplyTracing(ctx, tcfg, telcfg, logger)
	require.NotNil(t, tp)
	require.NotNil(t, deferFunc)

	// Global provider should now be the one we created.
	assert.Same(t, tp, otel.GetTracerProvider())

	// Shutdown via the returned deferFunc; with no spans queued, the
	// batch processor shuts down promptly without contacting the endpoint.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()
	require.NoError(t, tp.Shutdown(shutdownCtx))
}

// Covers the Lightstep-token branch of newExporter via ApplyTracing.
func TestApplyTracing_LightstepHeader(t *testing.T) {
	logger := zap.NewNop()
	tcfg := &config.TracingConfig{
		Enabled:              true,
		Endpoint:             "127.0.0.1:0",
		Insecure:             true,
		LightstepAccessToken: "test-token",
	}
	telcfg := &config.TelemetryConfig{
		ServiceName:               "reval-test",
		ServiceVersion:            "0.0.0-test",
		DeploymentEnvironmentName: "unit",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, tp := tracing.ApplyTracing(ctx, tcfg, telcfg, logger)
	require.NotNil(t, tp)

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()
	require.NoError(t, tp.Shutdown(shutdownCtx))
}
