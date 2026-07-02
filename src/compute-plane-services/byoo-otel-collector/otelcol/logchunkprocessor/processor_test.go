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
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/processor"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.uber.org/zap"
)

func newTestProcessor(t *testing.T, cfg *Config) *logChunkProcessor {
	t.Helper()

	return newTestProcessorWithMeterProvider(t, cfg, noop.NewMeterProvider())
}

func newTestProcessorWithMeterProvider(t *testing.T, cfg *Config, meterProvider metric.MeterProvider) *logChunkProcessor {
	t.Helper()

	p, err := newProcessor(processor.Settings{
		ID: component.MustNewID(typeStr),
		TelemetrySettings: component.TelemetrySettings{
			Logger:        zap.NewNop(),
			MeterProvider: meterProvider,
		},
	}, cfg)
	require.NoError(t, err)
	p.newChunkID = func(plog.LogRecord, string) (string, error) {
		return "chunk-id", nil
	}
	return p
}

func makeLogs(body pcommon.Value) plog.Logs {
	logs := plog.NewLogs()
	resourceLogs := logs.ResourceLogs().AppendEmpty()
	resourceLogs.Resource().Attributes().PutStr("service.name", "test-service")
	scopeLogs := resourceLogs.ScopeLogs().AppendEmpty()
	scopeLogs.Scope().SetName("test-scope")
	record := scopeLogs.LogRecords().AppendEmpty()
	record.SetTimestamp(123)
	record.SetObservedTimestamp(456)
	record.SetSeverityText("INFO")
	record.Attributes().PutStr("existing", "value")
	body.CopyTo(record.Body())
	return logs
}

func firstRecord(logs plog.Logs) plog.LogRecord {
	return logs.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0)
}

func TestDisabledLeavesLogsUnchanged(t *testing.T) {
	processor := newTestProcessor(t, &Config{MaxBodyBytes: 0, MetadataPrefix: defaultMetadataPrefix})
	input := makeLogs(pcommon.NewValueStr("abcdef"))

	output, err := processor.processLogs(context.Background(), input)

	require.NoError(t, err)
	require.Equal(t, 1, output.LogRecordCount())
	record := firstRecord(output)
	require.Equal(t, "abcdef", record.Body().Str())
	_, ok := record.Attributes().Get(defaultMetadataPrefix + ".id")
	require.False(t, ok)
}

func TestValidateRejectsEnabledLimitBelowUTF8Max(t *testing.T) {
	cfg := &Config{MaxBodyBytes: utf8.UTFMax - 1, MetadataPrefix: defaultMetadataPrefix}

	err := cfg.Validate()

	require.ErrorContains(t, err, "max_body_bytes must be 0 or at least 4")
}

func TestEnabledReturnsOriginalLogsWhenNoRecordsAreOversized(t *testing.T) {
	processor := newTestProcessor(t, &Config{MaxBodyBytes: 10, MetadataPrefix: defaultMetadataPrefix})
	input := makeLogs(pcommon.NewValueStr("abcdef"))

	output, err := processor.processLogs(context.Background(), input)

	require.NoError(t, err)
	requireSharesFirstRecord(t, input, output)
}

func TestDryRunLeavesOversizedLogsUnchanged(t *testing.T) {
	processor := newTestProcessor(t, &Config{MaxBodyBytes: 4, DryRun: true, MetadataPrefix: defaultMetadataPrefix})
	input := makeLogs(pcommon.NewValueStr("abcdef"))

	output, err := processor.processLogs(context.Background(), input)

	require.NoError(t, err)
	require.Equal(t, 1, output.LogRecordCount())
	record := firstRecord(output)
	require.Equal(t, "abcdef", record.Body().Str())
	_, ok := record.Attributes().Get(defaultMetadataPrefix + ".id")
	require.False(t, ok)
	requireSharesFirstRecord(t, input, output)
}

func TestDryRunMetricsUseModeAttribute(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	processor := newTestProcessorWithMeterProvider(t, &Config{
		MaxBodyBytes:   4,
		DryRun:         true,
		MetadataPrefix: defaultMetadataPrefix,
	}, meterProvider)
	ctx := context.Background()
	input := makeLogs(pcommon.NewValueStr("abcdef"))

	output, err := processor.processLogs(ctx, input)
	require.NoError(t, err)
	require.Equal(t, "abcdef", firstRecord(output).Body().Str())

	var metrics metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(ctx, &metrics))

	oversize := requireInt64MetricDataPoint(t, metrics, "otelcol_processor_logchunk_oversize_records_total", "dry_run")
	require.Equal(t, int64(1), oversize.Value)
	originalBytes := requireInt64MetricDataPoint(t, metrics, "otelcol_processor_logchunk_original_bytes_total", "dry_run")
	require.Equal(t, int64(6), originalBytes.Value)
	requireNoMetric(t, metrics, "otelcol_processor_logchunk_chunks_total")
	requireNoMetric(t, metrics, "otelcol_processor_logchunk_output_bytes_total")
}

func TestChunkMetricsUseModeAttribute(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	processor := newTestProcessorWithMeterProvider(t, &Config{
		MaxBodyBytes:   4,
		MetadataPrefix: defaultMetadataPrefix,
	}, meterProvider)
	ctx := context.Background()
	input := makeLogs(pcommon.NewValueStr("abcdef"))

	_, err := processor.processLogs(ctx, input)
	require.NoError(t, err)

	var metrics metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(ctx, &metrics))

	oversize := requireInt64MetricDataPoint(t, metrics, "otelcol_processor_logchunk_oversize_records_total", "chunk")
	require.Equal(t, int64(1), oversize.Value)
	chunks := requireInt64MetricDataPoint(t, metrics, "otelcol_processor_logchunk_chunks_total", "chunk")
	require.Equal(t, int64(2), chunks.Value)
	outputBytes := requireInt64MetricDataPoint(t, metrics, "otelcol_processor_logchunk_output_bytes_total", "chunk")
	require.Equal(t, int64(6), outputBytes.Value)
}

func TestChunksOversizedStringBody(t *testing.T) {
	processor := newTestProcessor(t, &Config{MaxBodyBytes: 5, MetadataPrefix: defaultMetadataPrefix})
	input := makeLogs(pcommon.NewValueStr("abcdefghijkl"))

	output, err := processor.processLogs(context.Background(), input)

	require.NoError(t, err)
	records := output.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords()
	require.Equal(t, 3, records.Len())
	require.Equal(t, "abcde", records.At(0).Body().Str())
	require.Equal(t, "fghij", records.At(1).Body().Str())
	require.Equal(t, "kl", records.At(2).Body().Str())

	for i := 0; i < records.Len(); i++ {
		attrs := records.At(i).Attributes()
		requireAttributeStr(t, attrs, defaultMetadataPrefix+".id", "chunk-id")
		requireAttributeInt(t, attrs, defaultMetadataPrefix+".index", int64(i))
		requireAttributeInt(t, attrs, defaultMetadataPrefix+".count", 3)
		requireAttributeInt(t, attrs, defaultMetadataPrefix+".original_size_bytes", 12)
		requireAttributeBool(t, attrs, defaultMetadataPrefix+".final", i == 2)
		requireAttributeStr(t, attrs, "existing", "value")
		requireAttributeAbsent(t, attrs, "_partial")
	}
	requireAttributeInt(t, records.At(0).Attributes(), defaultMetadataPrefix+".offset_bytes", 0)
	requireAttributeInt(t, records.At(1).Attributes(), defaultMetadataPrefix+".offset_bytes", 5)
	requireAttributeInt(t, records.At(2).Attributes(), defaultMetadataPrefix+".offset_bytes", 10)
}

func TestChunksDoNotSplitUTF8Runes(t *testing.T) {
	chunks := splitUTF8ByBytes("ab🙂cd", 5)

	require.Equal(t, []string{"ab", "🙂c", "d"}, chunks)
	for _, chunk := range chunks {
		require.True(t, utf8.ValidString(chunk), "chunk %q is invalid UTF-8", chunk)
		require.LessOrEqual(t, len(chunk), 5)
	}
}

func TestChunksFitFourByteUTF8RunesWithinLimit(t *testing.T) {
	chunks := splitUTF8ByBytes("🙂🙂", utf8.UTFMax)

	require.Equal(t, []string{"🙂", "🙂"}, chunks)
	for _, chunk := range chunks {
		require.True(t, utf8.ValidString(chunk), "chunk %q is invalid UTF-8", chunk)
		require.LessOrEqual(t, len(chunk), utf8.UTFMax)
	}
}

func TestNonStringBodyIsUnchanged(t *testing.T) {
	body := pcommon.NewValueMap()
	body.Map().PutStr("message", "abcdef")
	processor := newTestProcessor(t, &Config{MaxBodyBytes: 4, MetadataPrefix: defaultMetadataPrefix})
	input := makeLogs(body)

	output, err := processor.processLogs(context.Background(), input)

	require.NoError(t, err)
	require.Equal(t, 1, output.LogRecordCount())
	record := firstRecord(output)
	require.Equal(t, pcommon.ValueTypeMap, record.Body().Type())
	value, ok := record.Body().Map().Get("message")
	require.True(t, ok)
	require.Equal(t, "abcdef", value.Str())
	_, ok = record.Attributes().Get(defaultMetadataPrefix + ".id")
	require.False(t, ok)
}

func requireAttributeStr(t *testing.T, attrs pcommon.Map, key string, want string) {
	t.Helper()
	got, ok := attrs.Get(key)
	require.True(t, ok, "missing attribute %s", key)
	require.Equal(t, want, got.Str())
}

func requireAttributeInt(t *testing.T, attrs pcommon.Map, key string, want int64) {
	t.Helper()
	got, ok := attrs.Get(key)
	require.True(t, ok, "missing attribute %s", key)
	require.Equal(t, want, got.Int())
}

func requireAttributeBool(t *testing.T, attrs pcommon.Map, key string, want bool) {
	t.Helper()
	got, ok := attrs.Get(key)
	require.True(t, ok, "missing attribute %s", key)
	require.Equal(t, want, got.Bool())
}

func requireAttributeAbsent(t *testing.T, attrs pcommon.Map, key string) {
	t.Helper()
	_, ok := attrs.Get(key)
	require.False(t, ok, "attribute %s should not be present", key)
}

func requireSharesFirstRecord(t *testing.T, input plog.Logs, output plog.Logs) {
	t.Helper()
	firstRecord(output).Attributes().PutStr("shared", "true")
	got, ok := firstRecord(input).Attributes().Get("shared")
	require.True(t, ok, "output should share the original log record")
	require.Equal(t, "true", got.Str())
}

func requireInt64MetricDataPoint(
	t *testing.T,
	metrics metricdata.ResourceMetrics,
	name string,
	mode string,
) metricdata.DataPoint[int64] {
	t.Helper()
	for _, scopeMetrics := range metrics.ScopeMetrics {
		for _, current := range scopeMetrics.Metrics {
			if current.Name != name {
				continue
			}
			sum, ok := current.Data.(metricdata.Sum[int64])
			require.True(t, ok, "metric %s has unexpected data type %T", name, current.Data)
			for _, point := range sum.DataPoints {
				if hasMetricMode(point.Attributes, mode) {
					return point
				}
			}
		}
	}
	t.Fatalf("missing metric %s with mode=%q and no dry_run attribute", name, mode)
	return metricdata.DataPoint[int64]{}
}

func requireNoMetric(t *testing.T, metrics metricdata.ResourceMetrics, name string) {
	t.Helper()
	for _, scopeMetrics := range metrics.ScopeMetrics {
		for _, current := range scopeMetrics.Metrics {
			require.NotEqual(t, name, current.Name, "metric %s should not be recorded", name)
		}
	}
}

func hasMetricMode(attrs attribute.Set, mode string) bool {
	var gotMode string
	var gotModeOK bool
	var hasDryRun bool
	for _, attr := range attrs.ToSlice() {
		switch string(attr.Key) {
		case "mode":
			gotMode = attr.Value.AsString()
			gotModeOK = true
		case "dry_run":
			hasDryRun = true
		}
	}
	return gotModeOK && gotMode == mode && !hasDryRun
}
