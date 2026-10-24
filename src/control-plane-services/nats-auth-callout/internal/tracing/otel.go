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
	"strconv"
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

// OtelProvider implements the TracingProvider interface for OpenTelemetry
type OtelProvider struct {
	logger   *zap.Logger
	config   OtelTracingConfig
	shutdown func(context.Context) error
}

// NewOtelProvider creates a new OpenTelemetry tracing provider
func NewOtelProvider(logger *zap.Logger, config OtelTracingConfig) *OtelProvider {
	return &OtelProvider{
		logger: logger,
		config: config,
	}
}

// InitTracing initializes OpenTelemetry tracing
func (o *OtelProvider) InitTracing(ctx context.Context, serviceName, serviceVersion string) (func(context.Context) error, error) {
	o.logger.Info("Initializing OpenTelemetry tracing",
		zap.String("provider", "otel"),
		zap.String("service", serviceName),
		zap.String("version", serviceVersion),
		zap.String("endpoint", o.getEffectiveEndpoint()))

	// Create resource with comprehensive attributes
	resourceAttributes := []resource.Option{
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(serviceVersion),
		),
	}

	// Auto-detect runtime attributes
	runtimeAttributes := o.autoDetectRuntimeAttributes()
	resourceAttributes = append(resourceAttributes, runtimeAttributes...)

	res, err := resource.New(ctx, resourceAttributes...)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}

	// Create OTLP exporter
	exporter, err := o.createExporter(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create exporter: %w", err)
	}

	// Create trace provider with sampling configuration
	traceProviderOptions := []trace.TracerProviderOption{
		trace.WithBatcher(exporter,
			trace.WithBatchTimeout(time.Duration(o.config.ScheduleDelayMs)*time.Millisecond),
			trace.WithMaxExportBatchSize(o.config.MaxExportBatchSize),
			trace.WithMaxQueueSize(o.config.MaxQueueSize),
			trace.WithExportTimeout(time.Duration(o.config.ExportTimeoutMs)*time.Millisecond),
		),
		trace.WithResource(res),
	}

	// Add sampling if configured
	samplingRatio, err := parseSamplingRatio(o.config.SamplingRatio)
	if err != nil {
		o.logger.Warn("Invalid sampling ratio, using default (no sampling)",
			zap.String("sampling_ratio", o.config.SamplingRatio),
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

	o.logger.Info("OpenTelemetry tracing initialized successfully",
		zap.String("sampling_ratio", o.config.SamplingRatio),
		zap.Int("max_export_batch_size", o.config.MaxExportBatchSize),
		zap.Int("max_queue_size", o.config.MaxQueueSize))

	// Store shutdown function
	o.shutdown = tp.Shutdown
	return tp.Shutdown, nil
}

// GetProviderInfo returns provider information for logging
func (o *OtelProvider) GetProviderInfo() string {
	return fmt.Sprintf("OpenTelemetry - Endpoint: %s, Environment: %s",
		o.getEffectiveEndpoint(), o.config.Environment)
}

// Shutdown gracefully shuts down the OpenTelemetry provider
func (o *OtelProvider) Shutdown(ctx context.Context) error {
	if o.shutdown != nil {
		return o.shutdown(ctx)
	}
	return nil
}

// getEffectiveEndpoint returns the appropriate endpoint based on configuration
func (o *OtelProvider) getEffectiveEndpoint() string {
	if o.config.HTTPEndpoint != "" {
		return o.config.HTTPEndpoint
	}
	return o.config.Endpoint
}

// createExporter creates an OTLP HTTP exporter based on the configuration
func (o *OtelProvider) createExporter(ctx context.Context) (trace.SpanExporter, error) {
	endpoint := o.getEffectiveEndpoint()
	if endpoint == "" {
		return nil, fmt.Errorf("no tracing endpoint configured")
	}

	options := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(endpoint),
		otlptracehttp.WithTimeout(time.Duration(o.config.TimeoutMs) * time.Millisecond),
	}

	// Configure retry settings if specified
	if o.config.RetryDelayMs > 0 {
		// Note: WithRetry option configures exponential backoff retry mechanism
		options = append(options, otlptracehttp.WithRetry(otlptracehttp.RetryConfig{
			Enabled:         true,
			InitialInterval: time.Duration(o.config.RetryDelayMs) * time.Millisecond,
			MaxInterval:     time.Duration(o.config.RetryDelayMs*4) * time.Millisecond, // Max 4x initial
			MaxElapsedTime:  time.Duration(o.config.TimeoutMs) * time.Millisecond,      // Use timeout as max elapsed
		}))
	}

	// Configure insecure connection if specified
	if o.config.Insecure {
		options = append(options, otlptracehttp.WithInsecure())
	}

	// Configure compression
	if o.config.Compression != "" {
		switch strings.ToLower(o.config.Compression) {
		case "gzip":
			options = append(options, otlptracehttp.WithCompression(otlptracehttp.GzipCompression))
		case "none":
			options = append(options, otlptracehttp.WithCompression(otlptracehttp.NoCompression))
		default:
			o.logger.Warn("Unknown compression type, using default", zap.String("compression", o.config.Compression))
		}
	}

	// Add custom headers
	headers := make(map[string]string)
	if o.config.Headers.Authorization != "" {
		headers["Authorization"] = o.config.Headers.Authorization
	}
	if o.config.Headers.XAPIKey != "" {
		headers["X-API-Key"] = o.config.Headers.XAPIKey
	}

	if len(headers) > 0 {
		options = append(options, otlptracehttp.WithHeaders(headers))
		o.logger.Info("Configured OTLP exporter with custom headers")
	}

	o.logger.Info("Creating OTLP HTTP exporter",
		zap.String("endpoint", endpoint),
		zap.Bool("insecure", o.config.Insecure),
		zap.String("compression", o.config.Compression),
		zap.Int("timeout_ms", o.config.TimeoutMs),
		zap.Int("retry_delay_ms", o.config.RetryDelayMs))

	return otlptracehttp.New(ctx, options...)
}

// autoDetectRuntimeAttributes automatically detects runtime attributes from the environment
func (o *OtelProvider) autoDetectRuntimeAttributes() []resource.Option {
	var attrs []resource.Option

	// Auto-detect Kubernetes pod name (POD_NAME is preferred, HOSTNAME as fallback)
	podName := os.Getenv("POD_NAME")
	if podName == "" {
		podName = os.Getenv("HOSTNAME")
	}
	if podName != "" {
		attrs = append(attrs, resource.WithAttributes(semconv.K8SPodName(podName)))
		o.logger.Debug("Auto-detected Kubernetes pod name", zap.String("pod_name", podName))
	}

	// Auto-detect Kubernetes namespace
	if namespace := os.Getenv("POD_NAMESPACE"); namespace != "" {
		attrs = append(attrs, resource.WithAttributes(semconv.K8SNamespaceName(namespace)))
		o.logger.Debug("Auto-detected Kubernetes namespace", zap.String("namespace", namespace))
	}

	// Auto-detect Kubernetes node name
	if nodeName := os.Getenv("NODE_NAME"); nodeName != "" {
		attrs = append(attrs, resource.WithAttributes(semconv.K8SNodeName(nodeName)))
		o.logger.Debug("Auto-detected Kubernetes node name", zap.String("node_name", nodeName))
	}

	// Auto-detect Kubernetes cluster name (if available)
	if clusterName := os.Getenv("CLUSTER_NAME"); clusterName != "" {
		attrs = append(attrs, resource.WithAttributes(semconv.K8SClusterName(clusterName)))
		o.logger.Debug("Auto-detected Kubernetes cluster name", zap.String("cluster_name", clusterName))
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
		o.logger.Debug("Auto-detected service instance ID", zap.String("instance_id", instanceID))
	}

	return attrs
}

// parseSamplingRatio parses a string sampling ratio to float64
func parseSamplingRatio(samplingRatioStr string) (float64, error) {
	if samplingRatioStr == "" {
		return 0, nil
	}

	samplingRatio, err := strconv.ParseFloat(samplingRatioStr, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid sampling ratio format: %w", err)
	}

	if samplingRatio < 0 || samplingRatio > 1.0 {
		return 0, fmt.Errorf("sampling ratio must be between 0.0 and 1.0, got %f", samplingRatio)
	}

	return samplingRatio, nil
}
