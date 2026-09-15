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

package tracing

import (
	"context"
	"fmt"

	nvkittracing "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/tracing"
	"github.com/uptrace/opentelemetry-go-extra/otelzap"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/observability/common"
)

func ApplyTracing(ctx context.Context, tracingConfig *TracingConfig, telemetryConfig *common.TelemetryConfig, logger *otelzap.Logger) func() {
	if !tracingConfig.Enabled {
		logger.Warn("tracing disabled")
		return func() {}
	}

	endpoint, token, err := tracingEndpoint(tracingConfig)
	if err != nil {
		logger.Fatal("invalid tracing configuration", zap.Error(err))
	}

	logger.Warn("creating new otlp exporter",
		zap.String("provider", string(tracingConfig.Provider)),
		zap.String("endpoint", endpoint),
		zap.Bool("https", tracingConfig.Https),
	)

	_, err = nvkittracing.SetupOTELTracer(&nvkittracing.OTELConfig{
		Enabled:     true,
		Endpoint:    endpoint,
		AccessToken: token,
		Insecure:    !tracingConfig.Https,
		Attributes: nvkittracing.Attributes{
			ServiceName:    telemetryConfig.ServiceName,
			ServiceVersion: telemetryConfig.ServiceVersion,
			Extra: map[string]string{
				"deployment.environment.name": telemetryConfig.EnvironmentName,
			},
		},
	})
	if err != nil {
		logger.Fatal("could not create otel tracer", zap.Error(err))
	}

	return func() {
		_ = ctx
		nvkittracing.Shutdown()
	}
}

func tracingEndpoint(cfg *TracingConfig) (endpoint string, token string, err error) {
	switch cfg.Provider {
	case "", Jaeger:
		endpoint = cfg.Jaeger.Endpoint
	case Lightstep:
		endpoint = cfg.Lightstep.Endpoint
		token = cfg.Lightstep.Token
	case OTLP:
		endpoint = cfg.OTLP.Endpoint
	default:
		err = fmt.Errorf("unknown tracing provider: %s", cfg.Provider)
	}
	if endpoint == "" && err == nil {
		err = fmt.Errorf("tracing endpoint is required for provider %s", cfg.Provider)
	}
	return endpoint, token, err
}
