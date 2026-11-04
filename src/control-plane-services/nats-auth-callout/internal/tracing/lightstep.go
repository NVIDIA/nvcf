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
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
	"go.uber.org/zap"
)

// LightstepProvider implements the TracingProvider interface for Lightstep
type LightstepProvider struct {
	logger   *zap.Logger
	config   LightstepTracingConfig
	shutdown func(context.Context) error
}

// NewLightstepProvider creates a new Lightstep tracing provider
func NewLightstepProvider(logger *zap.Logger, config LightstepTracingConfig) *LightstepProvider {
	return &LightstepProvider{
		logger: logger,
		config: config,
	}
}

// InitTracing initializes Lightstep tracing
func (l *LightstepProvider) InitTracing(ctx context.Context, serviceName, serviceVersion string) (func(context.Context) error, error) {
	l.logger.Info("Initializing Lightstep tracing",
		zap.String("provider", "lightstep"),
		zap.String("service", serviceName),
		zap.String("version", serviceVersion),
		zap.String("endpoint", l.config.Endpoint))

	// Create resource with comprehensive attributes
	resourceAttributes := []resource.Option{
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(serviceVersion),
		),
	}

	// Auto-detect runtime attributes
	runtimeAttributes := l.autoDetectRuntimeAttributes()
	resourceAttributes = append(resourceAttributes, runtimeAttributes...)

	res, err := resource.New(ctx, resourceAttributes...)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}

	// Create Lightstep-specific OTLP exporter
	exporter, err := l.createLightstepExporter(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create Lightstep exporter: %w", err)
	}

	// Create trace provider with sampling configuration
	traceProviderOptions := []trace.TracerProviderOption{
		trace.WithBatcher(exporter,
			trace.WithBatchTimeout(time.Duration(l.config.ScheduleDelayMs)*time.Millisecond),
			trace.WithMaxExportBatchSize(l.config.MaxExportBatchSize),
			trace.WithMaxQueueSize(l.config.MaxQueueSize),
			trace.WithExportTimeout(time.Duration(l.config.ExportTimeoutMs)*time.Millisecond),
		),
		trace.WithResource(res),
	}

	// Add sampling if configured
	samplingRatio, err := parseSamplingRatio(l.config.SamplingRatio)
	if err != nil {
		l.logger.Warn("Invalid sampling ratio, using default (no sampling)",
			zap.String("sampling_ratio", l.config.SamplingRatio),
			zap.Error(err))
	} else if samplingRatio > 0 && samplingRatio <= 1.0 {
		traceProviderOptions = append(traceProviderOptions,
			trace.WithSampler(trace.TraceIDRatioBased(samplingRatio)))
	}

	tp := trace.NewTracerProvider(traceProviderOptions...)

	// Set global trace provider
	otel.SetTracerProvider(tp)

	// Set global propagator
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	l.logger.Info("Lightstep tracing initialized successfully",
		zap.String("sampling_ratio", l.config.SamplingRatio),
		zap.Int("max_export_batch_size", l.config.MaxExportBatchSize),
		zap.Int("max_queue_size", l.config.MaxQueueSize))

	// Store shutdown function
	l.shutdown = tp.Shutdown
	return tp.Shutdown, nil
}

// GetProviderInfo returns provider information for logging
func (l *LightstepProvider) GetProviderInfo() string {
	return fmt.Sprintf("Lightstep - Endpoint: %s, Environment: %s",
		l.config.Endpoint, l.config.Environment)
}

// Shutdown gracefully shuts down the Lightstep provider
func (l *LightstepProvider) Shutdown(ctx context.Context) error {
	if l.shutdown != nil {
		return l.shutdown(ctx)
	}
	return nil
}

// createLightstepExporter creates a Lightstep-specific OTLP HTTP exporter
func (l *LightstepProvider) createLightstepExporter(ctx context.Context) (trace.SpanExporter, error) {
	if l.config.Endpoint == "" {
		return nil, fmt.Errorf("no Lightstep endpoint configured")
	}

	options := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(l.config.Endpoint),
		otlptracehttp.WithTimeout(time.Duration(l.config.TimeoutMs) * time.Millisecond),
	}

	// Configure insecure connection if specified
	if l.config.Insecure {
		options = append(options, otlptracehttp.WithInsecure())
	}

	// Configure compression
	if l.config.Compression != "" {
		switch strings.ToLower(l.config.Compression) {
		case "gzip":
			options = append(options, otlptracehttp.WithCompression(otlptracehttp.GzipCompression))
		case "none":
			options = append(options, otlptracehttp.WithCompression(otlptracehttp.NoCompression))
		default:
			l.logger.Warn("Unknown compression type, using default", zap.String("compression", l.config.Compression))
		}
	}

	// Add Lightstep-specific headers
	headers := make(map[string]string)
	if l.config.AccessToken != "" {
		headers["lightstep-access-token"] = l.config.AccessToken
	}

	if len(headers) > 0 {
		options = append(options, otlptracehttp.WithHeaders(headers))
		l.logger.Info("Configured Lightstep exporter with access token")
	}

	l.logger.Info("Creating Lightstep OTLP HTTP exporter",
		zap.String("endpoint", l.config.Endpoint),
		zap.Bool("insecure", l.config.Insecure),
		zap.String("compression", l.config.Compression),
		zap.Int("timeout_ms", l.config.TimeoutMs))

	return otlptracehttp.New(ctx, options...)
}

// autoDetectRuntimeAttributes automatically detects runtime attributes from the environment
func (l *LightstepProvider) autoDetectRuntimeAttributes() []resource.Option {
	var attrs []resource.Option

	// Auto-detect Kubernetes pod name (POD_NAME is preferred, HOSTNAME as fallback)
	podName := os.Getenv("POD_NAME")
	if podName == "" {
		podName = os.Getenv("HOSTNAME")
	}
	if podName != "" {
		attrs = append(attrs, resource.WithAttributes(semconv.K8SPodName(podName)))
		l.logger.Debug("Auto-detected Kubernetes pod name", zap.String("pod_name", podName))
	}

	// Auto-detect Kubernetes namespace
	if namespace := os.Getenv("POD_NAMESPACE"); namespace != "" {
		attrs = append(attrs, resource.WithAttributes(semconv.K8SNamespaceName(namespace)))
		l.logger.Debug("Auto-detected Kubernetes namespace", zap.String("namespace", namespace))
	}

	// Auto-detect Kubernetes node name
	if nodeName := os.Getenv("NODE_NAME"); nodeName != "" {
		attrs = append(attrs, resource.WithAttributes(semconv.K8SNodeName(nodeName)))
		l.logger.Debug("Auto-detected Kubernetes node name", zap.String("node_name", nodeName))
	}

	// Auto-detect Kubernetes cluster name (if available)
	if clusterName := os.Getenv("CLUSTER_NAME"); clusterName != "" {
		attrs = append(attrs, resource.WithAttributes(semconv.K8SClusterName(clusterName)))
		l.logger.Debug("Auto-detected Kubernetes cluster name", zap.String("cluster_name", clusterName))
	}

	// Generate service instance ID (prefer pod name, fallback to hostname)
	instanceID := podName
	if instanceID == "" {
		if hostname, err := os.Hostname(); err == nil && hostname != "" {
			instanceID = hostname
		}
	}
	if instanceID != "" {
		attrs = append(attrs, resource.WithAttributes(semconv.ServiceInstanceID(instanceID)))
		l.logger.Debug("Auto-detected service instance ID", zap.String("instance_id", instanceID))
	}

	return attrs
}
