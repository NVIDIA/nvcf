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
	"fmt"
	"strings"

	"go.uber.org/zap"
)

// TracingConfig holds the tracing configuration (mirrors config.TracingConfig)
type TracingConfig struct {
	Provider  string                 `mapstructure:"provider"`
	Enabled   bool                   `mapstructure:"enabled"`
	Otel      OtelTracingConfig      `mapstructure:"otel"`
	Lightstep LightstepTracingConfig `mapstructure:"lightstep"`
}

// OtelTracingConfig holds OpenTelemetry-specific tracing configuration for the factory
type OtelTracingConfig struct {
	Endpoint           string               `mapstructure:"endpoint"`
	HTTPEndpoint       string               `mapstructure:"http_endpoint"`
	Insecure           bool                 `mapstructure:"insecure"`
	Environment        string               `mapstructure:"environment"`
	SamplingRatio      string               `mapstructure:"sampling_ratio"`
	Headers            TracingHeadersConfig `mapstructure:"headers"`
	TimeoutMs          int                  `mapstructure:"timeout_ms"`
	RetryDelayMs       int                  `mapstructure:"retry_delay_ms"`
	MaxExportBatchSize int                  `mapstructure:"max_export_batch_size"`
	ExportTimeoutMs    int                  `mapstructure:"export_timeout_ms"`
	MaxQueueSize       int                  `mapstructure:"max_queue_size"`
	ScheduleDelayMs    int                  `mapstructure:"schedule_delay_ms"`
	Compression        string               `mapstructure:"compression"`
}

// LightstepTracingConfig holds Lightstep-specific tracing configuration for the factory
type LightstepTracingConfig struct {
	Endpoint           string `mapstructure:"endpoint"`
	AccessToken        string `mapstructure:"access_token"`
	Environment        string `mapstructure:"environment"`
	SamplingRatio      string `mapstructure:"sampling_ratio"`
	TimeoutMs          int    `mapstructure:"timeout_ms"`
	MaxExportBatchSize int    `mapstructure:"max_export_batch_size"`
	ExportTimeoutMs    int    `mapstructure:"export_timeout_ms"`
	MaxQueueSize       int    `mapstructure:"max_queue_size"`
	ScheduleDelayMs    int    `mapstructure:"schedule_delay_ms"`
	Compression        string `mapstructure:"compression"`
	Insecure           bool   `mapstructure:"insecure"`
}

// TracingHeadersConfig holds tracing headers configuration
type TracingHeadersConfig struct {
	Authorization string `mapstructure:"authorization"`
	XAPIKey       string `mapstructure:"x-api-key"`
}

// NewTracingProvider creates a new tracing provider based on the configuration
func NewTracingProvider(logger *zap.Logger, config TracingConfig) (TracingProvider, error) {
	if !config.Enabled {
		return NewNoOpProvider(logger), nil
	}

	provider := strings.ToLower(config.Provider)

	switch provider {
	case "otel", "opentelemetry":
		return NewOtelProvider(logger, config.Otel), nil
	case "lightstep":
		return NewLightstepProvider(logger, config.Lightstep), nil
	case "":
		return nil, fmt.Errorf("tracing provider not specified")
	default:
		return nil, fmt.Errorf("unsupported tracing provider: %s", provider)
	}
}
