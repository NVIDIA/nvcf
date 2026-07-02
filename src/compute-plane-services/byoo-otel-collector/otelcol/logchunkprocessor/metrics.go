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

package logchunkprocessor

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type processorMetrics struct {
	oversizeRecords metric.Int64Counter
	chunks          metric.Int64Counter
	originalBytes   metric.Int64Counter
	outputBytes     metric.Int64Counter
	errors          metric.Int64Counter
}

func newProcessorMetrics(meter metric.Meter) (processorMetrics, error) {
	oversizeRecords, err := meter.Int64Counter(
		"otelcol_processor_logchunk_oversize_records_total",
		metric.WithDescription("Number of log records whose bodies exceed the configured chunking limit."),
		metric.WithUnit("{records}"),
	)
	if err != nil {
		return processorMetrics{}, err
	}

	chunks, err := meter.Int64Counter(
		"otelcol_processor_logchunk_chunks_total",
		metric.WithDescription("Number of log chunks emitted by the log chunk processor."),
		metric.WithUnit("{chunks}"),
	)
	if err != nil {
		return processorMetrics{}, err
	}

	originalBytes, err := meter.Int64Counter(
		"otelcol_processor_logchunk_original_bytes_total",
		metric.WithDescription("Bytes observed in oversized original log bodies."),
		metric.WithUnit("By"),
	)
	if err != nil {
		return processorMetrics{}, err
	}

	outputBytes, err := meter.Int64Counter(
		"otelcol_processor_logchunk_output_bytes_total",
		metric.WithDescription("Bytes emitted across chunked log bodies."),
		metric.WithUnit("By"),
	)
	if err != nil {
		return processorMetrics{}, err
	}

	errors, err := meter.Int64Counter(
		"otelcol_processor_logchunk_errors_total",
		metric.WithDescription("Errors encountered while chunking oversized log records."),
		metric.WithUnit("{errors}"),
	)
	if err != nil {
		return processorMetrics{}, err
	}

	return processorMetrics{
		oversizeRecords: oversizeRecords,
		chunks:          chunks,
		originalBytes:   originalBytes,
		outputBytes:     outputBytes,
		errors:          errors,
	}, nil
}

func logChunkMetricAttributes(mode string) metric.MeasurementOption {
	return metric.WithAttributes(
		attribute.String("mode", mode),
	)
}

func (m processorMetrics) recordOversize(ctx context.Context, mode string, originalBytes int64) {
	opts := logChunkMetricAttributes(mode)
	m.oversizeRecords.Add(ctx, 1, opts)
	m.originalBytes.Add(ctx, originalBytes, opts)
}

func (m processorMetrics) recordChunks(ctx context.Context, mode string, count int64, outputBytes int64) {
	opts := logChunkMetricAttributes(mode)
	m.chunks.Add(ctx, count, opts)
	m.outputBytes.Add(ctx, outputBytes, opts)
}

func (m processorMetrics) recordError(ctx context.Context, mode string, reason string) {
	m.errors.Add(ctx, 1, metric.WithAttributes(
		attribute.String("mode", mode),
		attribute.String("reason", reason),
	))
}
