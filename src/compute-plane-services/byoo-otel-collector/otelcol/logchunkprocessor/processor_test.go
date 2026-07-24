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
	"strings"
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
	body.CopyTo(record.Body())
	return logs
}

func firstRecord(logs plog.Logs) plog.LogRecord {
	return logs.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0)
}

func TestDisabledLeavesLogsUnchanged(t *testing.T) {
	processor := newTestProcessor(t, &Config{MaxPayloadBytes: 0, MetadataPrefix: defaultMetadataPrefix})
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
	cfg := &Config{MaxPayloadBytes: utf8.UTFMax - 1, MetadataPrefix: defaultMetadataPrefix}

	err := cfg.Validate()

	require.ErrorContains(t, err, "max_payload_bytes must be 0 or at least 4")
}

func TestValidateUsesDeprecatedMaxBodyBytes(t *testing.T) {
	cfg := &Config{MaxBodyBytes: utf8.UTFMax - 1, MetadataPrefix: defaultMetadataPrefix}

	err := cfg.Validate()

	require.ErrorContains(t, err, "max_payload_bytes must be 0 or at least 4")
}

func TestDeprecatedMaxBodyBytesEnablesPayloadLimit(t *testing.T) {
	processor := newTestProcessor(t, &Config{MaxBodyBytes: 4, MetadataPrefix: defaultMetadataPrefix})
	input := makeLogs(pcommon.NewValueStr("abcdef"))

	output, err := processor.processLogs(context.Background(), input)

	require.NoError(t, err)
	records := output.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords()
	require.Equal(t, 2, records.Len())
	require.Equal(t, "abcd", records.At(0).Body().Str())
	require.Equal(t, "ef", records.At(1).Body().Str())
}

func TestMaxPayloadBytesTakesPrecedenceOverDeprecatedMaxBodyBytes(t *testing.T) {
	processor := newTestProcessor(t, &Config{
		MaxPayloadBytes: 6,
		MaxBodyBytes:    4,
		MetadataPrefix:  defaultMetadataPrefix,
	})
	input := makeLogs(pcommon.NewValueStr("abcdef"))

	output, err := processor.processLogs(context.Background(), input)

	require.NoError(t, err)
	requireSharesFirstRecord(t, input, output)
}

func TestEnabledReturnsOriginalLogsWhenNoRecordsAreOversized(t *testing.T) {
	processor := newTestProcessor(t, &Config{MaxPayloadBytes: 10, MetadataPrefix: defaultMetadataPrefix})
	input := makeLogs(pcommon.NewValueStr("abcdef"))

	output, err := processor.processLogs(context.Background(), input)

	require.NoError(t, err)
	requireSharesFirstRecord(t, input, output)
}

func TestDryRunLeavesOversizedLogsUnchanged(t *testing.T) {
	processor := newTestProcessor(t, &Config{MaxPayloadBytes: 4, DryRun: true, MetadataPrefix: defaultMetadataPrefix})
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
		MaxPayloadBytes: 4,
		DryRun:          true,
		MetadataPrefix:  defaultMetadataPrefix,
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
		MaxPayloadBytes: 4,
		MetadataPrefix:  defaultMetadataPrefix,
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
	processor := newTestProcessor(t, &Config{MaxPayloadBytes: 5, MetadataPrefix: defaultMetadataPrefix})
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
		requireAttributeAbsent(t, attrs, "_partial")
	}
	requireAttributeInt(t, records.At(0).Attributes(), defaultMetadataPrefix+".offset_bytes", 0)
	requireAttributeInt(t, records.At(1).Attributes(), defaultMetadataPrefix+".offset_bytes", 5)
	requireAttributeInt(t, records.At(2).Attributes(), defaultMetadataPrefix+".offset_bytes", 10)
}

func TestChunksStringBodyAndAttributesUnderSharedLimit(t *testing.T) {
	processor := newTestProcessor(t, &Config{MaxPayloadBytes: 8, MetadataPrefix: defaultMetadataPrefix})
	input := makeLogs(pcommon.NewValueStr("abcdefghijkl"))
	firstRecord(input).Attributes().PutStr("a", "value")
	firstRecord(input).Attributes().PutStr("b", "123456")

	output, err := processor.processLogs(context.Background(), input)

	require.NoError(t, err)
	records := output.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords()
	require.Equal(t, 4, records.Len())

	expectedBodies := []string{"abcdefgh", "ijkl", "", ""}
	expectedA := []string{"", "val", "ue", ""}
	expectedAOK := []bool{false, true, true, false}
	expectedB := []string{"", "", "1234", "56"}
	expectedBOK := []bool{false, false, true, true}
	for i := 0; i < records.Len(); i++ {
		record := records.At(i)
		require.Equal(t, expectedBodies[i], record.Body().Str())
		requireOptionalAttributeStr(t, record.Attributes(), "a", expectedA[i], expectedAOK[i])
		requireOptionalAttributeStr(t, record.Attributes(), "b", expectedB[i], expectedBOK[i])
		require.LessOrEqual(t, payloadBytes(record, defaultMetadataPrefix), 8)
		requireAttributeInt(t, record.Attributes(), defaultMetadataPrefix+".index", int64(i))
		requireAttributeInt(t, record.Attributes(), defaultMetadataPrefix+".count", 4)
		requireAttributeInt(t, record.Attributes(), defaultMetadataPrefix+".original_size_bytes", 25)
		requireAttributeBool(t, record.Attributes(), defaultMetadataPrefix+".final", i == 3)
	}
	requireAttributeInt(t, records.At(0).Attributes(), defaultMetadataPrefix+".offset_bytes", 0)
	requireAttributeInt(t, records.At(1).Attributes(), defaultMetadataPrefix+".offset_bytes", 8)
	requireAttributeInt(t, records.At(2).Attributes(), defaultMetadataPrefix+".offset_bytes", 16)
	requireAttributeInt(t, records.At(3).Attributes(), defaultMetadataPrefix+".offset_bytes", 24)
	require.Equal(t, "abcdefghijkl", concatenateBodies(records))
	require.Equal(t, "value", concatenateStringAttribute(records, "a"))
	require.Equal(t, "123456", concatenateStringAttribute(records, "b"))
}

func TestChunksWhenCombinedStringBodyAndAttributesExceedLimit(t *testing.T) {
	processor := newTestProcessor(t, &Config{MaxPayloadBytes: 5, MetadataPrefix: defaultMetadataPrefix})
	input := makeLogs(pcommon.NewValueStr("abcd"))
	firstRecord(input).Attributes().PutStr("x", "yz")

	output, err := processor.processLogs(context.Background(), input)

	require.NoError(t, err)
	records := output.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords()
	require.Equal(t, 2, records.Len())
	require.Equal(t, "abcd", records.At(0).Body().Str())
	requireAttributeAbsent(t, records.At(0).Attributes(), "x")
	require.LessOrEqual(t, payloadBytes(records.At(0), defaultMetadataPrefix), 5)
	require.Empty(t, records.At(1).Body().Str())
	requireAttributeStr(t, records.At(1).Attributes(), "x", "yz")
	require.LessOrEqual(t, payloadBytes(records.At(1), defaultMetadataPrefix), 5)
	requireAttributeInt(t, records.At(0).Attributes(), defaultMetadataPrefix+".original_size_bytes", 7)
	requireAttributeInt(t, records.At(1).Attributes(), defaultMetadataPrefix+".original_size_bytes", 7)
}

func TestChunksNonStringAttributesOnce(t *testing.T) {
	processor := newTestProcessor(t, &Config{MaxPayloadBytes: 5, MetadataPrefix: defaultMetadataPrefix})
	input := makeLogs(pcommon.NewValueStr("abcd"))
	firstRecord(input).Attributes().PutInt("n", 12)
	firstRecord(input).Attributes().PutBool("ok", false)

	output, err := processor.processLogs(context.Background(), input)

	require.NoError(t, err)
	records := output.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords()
	require.Equal(t, 3, records.Len())
	require.Equal(t, "abcd", records.At(0).Body().Str())
	requireAttributeAbsent(t, records.At(0).Attributes(), "n")
	requireAttributeAbsent(t, records.At(0).Attributes(), "ok")
	require.LessOrEqual(t, payloadBytes(records.At(0), defaultMetadataPrefix), 5)

	require.Empty(t, records.At(1).Body().Str())
	requireAttributeInt(t, records.At(1).Attributes(), "n", 12)
	requireAttributeAbsent(t, records.At(1).Attributes(), "ok")
	require.LessOrEqual(t, payloadBytes(records.At(1), defaultMetadataPrefix), 5)

	require.Empty(t, records.At(2).Body().Str())
	requireAttributeAbsent(t, records.At(2).Attributes(), "n")
	requireAttributeBool(t, records.At(2).Attributes(), "ok", false)
	require.Equal(t, 7, payloadBytes(records.At(2), defaultMetadataPrefix))
}

func TestChunksBytesAttributesWithoutChangingType(t *testing.T) {
	processor := newTestProcessor(t, &Config{MaxPayloadBytes: 8, MetadataPrefix: defaultMetadataPrefix})
	input := makeLogs(pcommon.NewValueStr("ab"))
	firstRecord(input).Attributes().PutEmptyBytes("bin").FromRaw([]byte{1, 2, 3, 4, 5, 6})

	output, err := processor.processLogs(context.Background(), input)

	require.NoError(t, err)
	records := output.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords()
	require.Equal(t, 2, records.Len())

	require.Equal(t, "ab", records.At(0).Body().Str())
	requireAttributeBytes(t, records.At(0).Attributes(), "bin", []byte{1, 2, 3})
	require.LessOrEqual(t, payloadBytes(records.At(0), defaultMetadataPrefix), 8)

	require.Empty(t, records.At(1).Body().Str())
	requireAttributeBytes(t, records.At(1).Attributes(), "bin", []byte{4, 5, 6})
	require.LessOrEqual(t, payloadBytes(records.At(1), defaultMetadataPrefix), 8)
	require.Equal(t, []byte{1, 2, 3, 4, 5, 6}, concatenateBytesAttribute(records, "bin"))
}

func TestChunksStructuredAttributesRecursivelyWithoutChangingType(t *testing.T) {
	processor := newTestProcessor(t, &Config{MaxPayloadBytes: 6, MetadataPrefix: defaultMetadataPrefix})
	input := makeLogs(pcommon.NewValueStr("abcd"))
	attrs := firstRecord(input).Attributes()
	nested := attrs.PutEmptyMap("m")
	nested.PutStr("x", "y")
	list := attrs.PutEmptySlice("s")
	list.AppendEmpty().SetStr("a")
	list.AppendEmpty().SetStr("b")
	originalMap, _ := attrs.Get("m")
	originalSlice, _ := attrs.Get("s")

	output, err := processor.processLogs(context.Background(), input)

	require.NoError(t, err)
	records := output.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords()
	require.Equal(t, 3, records.Len())
	for i := 0; i < records.Len(); i++ {
		require.LessOrEqual(t, payloadBytes(records.At(i), defaultMetadataPrefix), 6)
	}

	mergedMap := mergeStructuredAttribute(t, records, "m")
	require.Equal(t, pcommon.ValueTypeMap, mergedMap.Type())
	require.Equal(t, originalMap.AsRaw(), mergedMap.AsRaw())
	mergedSlice := mergeStructuredAttribute(t, records, "s")
	require.Equal(t, pcommon.ValueTypeSlice, mergedSlice.Type())
	require.Equal(t, originalSlice.AsRaw(), mergedSlice.AsRaw())
	requireStructuredPath(t, records, "/attributes/m/x")
	requireStructuredPath(t, records, "/attributes/s/0")
	requireStructuredPath(t, records, "/attributes/s/1")
}

func TestChunksStructuredBodyWhenAttributesForceChunking(t *testing.T) {
	body := pcommon.NewValueMap()
	body.Map().PutStr("x", "y")
	processor := newTestProcessor(t, &Config{MaxPayloadBytes: 5, MetadataPrefix: defaultMetadataPrefix})
	input := makeLogs(body)
	firstRecord(input).Attributes().PutStr("a", "12345")

	output, err := processor.processLogs(context.Background(), input)

	require.NoError(t, err)
	records := output.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords()
	require.Equal(t, 2, records.Len())
	for i := 0; i < records.Len(); i++ {
		require.LessOrEqual(t, payloadBytes(records.At(i), defaultMetadataPrefix), 5)
	}
	mergedBody := mergeStructuredBody(records)
	require.Equal(t, body.AsRaw(), mergedBody.AsRaw())
	require.Equal(t, "12345", concatenateStringAttribute(records, "a"))
	requireStructuredPath(t, records, "/body/x")
}

func TestChunksGiantSinglePayloadMapRepresentativeOfOTLPLogs(t *testing.T) {
	const limit = 256 * 1024
	processor := newTestProcessor(t, &Config{MaxPayloadBytes: limit, MetadataPrefix: defaultMetadataPrefix})
	input := makeLogs(pcommon.NewValueStr("openai.request"))
	payload := firstRecord(input).Attributes().PutEmptyMap("payload")
	payload.PutStr("model", "meta/llama-3.1-405b-instruct")
	payload.PutInt("max_tokens", 4096)
	payload.PutBool("stream", true)
	messages := payload.PutEmptySlice("messages")
	systemMessage := messages.AppendEmpty().SetEmptyMap()
	systemMessage.PutStr("role", "system")
	systemMessage.PutStr("content", "You are a helpful assistant.")
	userMessage := messages.AppendEmpty().SetEmptyMap()
	userMessage.PutStr("role", "user")
	userMessage.PutStr("content", strings.Repeat("representative prompt content 🙂 ", 50_000))
	originalPayload, _ := firstRecord(input).Attributes().Get("payload")
	require.Greater(t, valuePayloadBytes(originalPayload), 1024*1024)

	output, err := processor.processLogs(context.Background(), input)

	require.NoError(t, err)
	records := output.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords()
	require.Greater(t, records.Len(), 4)
	for i := 0; i < records.Len(); i++ {
		record := records.At(i)
		require.LessOrEqual(t, payloadBytes(record, defaultMetadataPrefix), limit)
		value, ok := record.Attributes().Get("payload")
		if ok {
			require.Equal(t, pcommon.ValueTypeMap, value.Type())
			requireNestedContentUTF8(t, value)
		}
	}

	mergedPayload := mergeStructuredAttribute(t, records, "payload")
	require.Equal(t, originalPayload.AsRaw(), mergedPayload.AsRaw())
	require.Greater(t, countStructuredPath(records, "/attributes/payload/messages/1/content"), 1)
}

func TestChunksOversizedNestedBytesAndEscapedMapKeys(t *testing.T) {
	processor := newTestProcessor(t, &Config{MaxPayloadBytes: 32, MetadataPrefix: defaultMetadataPrefix})
	input := makeLogs(pcommon.NewValueStr("x"))
	payload := firstRecord(input).Attributes().PutEmptyMap("pay/load")
	nested := payload.PutEmptyMap("a~b")
	nested.PutEmptyBytes("attachment").FromRaw([]byte(strings.Repeat("0123456789", 10)))
	originalPayload, _ := firstRecord(input).Attributes().Get("pay/load")

	output, err := processor.processLogs(context.Background(), input)

	require.NoError(t, err)
	records := output.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords()
	require.Greater(t, records.Len(), 1)
	for i := 0; i < records.Len(); i++ {
		require.LessOrEqual(t, payloadBytes(records.At(i), defaultMetadataPrefix), 32)
	}
	mergedPayload := mergeStructuredAttribute(t, records, "pay/load")
	require.Equal(t, originalPayload.AsRaw(), mergedPayload.AsRaw())
	require.Greater(t, countStructuredPath(records, "/attributes/pay~1load/a~0b/attachment"), 1)
}

func TestChunksEmptyStructuredValuesWithoutLosingType(t *testing.T) {
	processor := newTestProcessor(t, &Config{MaxPayloadBytes: 5, MetadataPrefix: defaultMetadataPrefix})
	input := makeLogs(pcommon.NewValueStr("abcdef"))
	attrs := firstRecord(input).Attributes()
	attrs.PutEmptyMap("m")
	attrs.PutEmptySlice("s")

	output, err := processor.processLogs(context.Background(), input)

	require.NoError(t, err)
	records := output.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords()
	require.Equal(t, 2, records.Len())
	emptyMap := mergeStructuredAttribute(t, records, "m")
	require.Equal(t, pcommon.ValueTypeMap, emptyMap.Type())
	require.Empty(t, emptyMap.Map().AsRaw())
	emptySlice := mergeStructuredAttribute(t, records, "s")
	require.Equal(t, pcommon.ValueTypeSlice, emptySlice.Type())
	require.Empty(t, emptySlice.Slice().AsRaw())
	requireStructuredPath(t, records, "/attributes/m")
	requireStructuredPath(t, records, "/attributes/s")
}

func TestChunksEmptyAttributesOnce(t *testing.T) {
	processor := newTestProcessor(t, &Config{MaxPayloadBytes: 5, MetadataPrefix: defaultMetadataPrefix})
	input := makeLogs(pcommon.NewValueStr("abcdef"))
	firstRecord(input).Attributes().PutStr("e", "")
	firstRecord(input).Attributes().PutEmptyBytes("z")

	output, err := processor.processLogs(context.Background(), input)

	require.NoError(t, err)
	records := output.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords()
	require.Equal(t, 2, records.Len())
	require.Equal(t, "abcde", records.At(0).Body().Str())
	requireAttributeAbsent(t, records.At(0).Attributes(), "e")
	requireAttributeAbsent(t, records.At(0).Attributes(), "z")
	require.Equal(t, "f", records.At(1).Body().Str())
	requireAttributeStr(t, records.At(1).Attributes(), "e", "")
	requireAttributeBytes(t, records.At(1).Attributes(), "z", nil)
	require.LessOrEqual(t, payloadBytes(records.At(1), defaultMetadataPrefix), 5)
}

func TestChunksStringAttributesWithoutSplittingUTF8Runes(t *testing.T) {
	processor := newTestProcessor(t, &Config{MaxPayloadBytes: 5, MetadataPrefix: defaultMetadataPrefix})
	input := makeLogs(pcommon.NewValueStr("ab"))
	firstRecord(input).Attributes().PutStr("u", "🙂🙂")

	output, err := processor.processLogs(context.Background(), input)

	require.NoError(t, err)
	records := output.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords()
	require.Equal(t, 3, records.Len())
	require.Equal(t, "ab", records.At(0).Body().Str())
	requireAttributeAbsent(t, records.At(0).Attributes(), "u")
	requireAttributeStr(t, records.At(1).Attributes(), "u", "🙂")
	requireAttributeStr(t, records.At(2).Attributes(), "u", "🙂")
	attr1, _ := records.At(1).Attributes().Get("u")
	attr2, _ := records.At(2).Attributes().Get("u")
	require.True(t, utf8.ValidString(attr1.Str()))
	require.True(t, utf8.ValidString(attr2.Str()))
	require.LessOrEqual(t, payloadBytes(records.At(1), defaultMetadataPrefix), 5)
	require.LessOrEqual(t, payloadBytes(records.At(2), defaultMetadataPrefix), 5)
}

func TestChunksAttributesInDeterministicKeyOrder(t *testing.T) {
	processor := newTestProcessor(t, &Config{MaxPayloadBytes: 4, MetadataPrefix: defaultMetadataPrefix})
	input := makeLogs(pcommon.NewValueStr("x"))
	firstRecord(input).Attributes().PutStr("b", "12")
	firstRecord(input).Attributes().PutStr("a", "34")

	output, err := processor.processLogs(context.Background(), input)

	require.NoError(t, err)
	records := output.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords()
	require.Equal(t, 2, records.Len())
	require.Equal(t, "x", records.At(0).Body().Str())
	requireAttributeStr(t, records.At(0).Attributes(), "a", "34")
	requireAttributeAbsent(t, records.At(0).Attributes(), "b")
	require.Empty(t, records.At(1).Body().Str())
	requireAttributeAbsent(t, records.At(1).Attributes(), "a")
	requireAttributeStr(t, records.At(1).Attributes(), "b", "12")
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

func TestChunksOversizedStructuredBody(t *testing.T) {
	body := pcommon.NewValueMap()
	body.Map().PutStr("message", "abcdef")
	processor := newTestProcessor(t, &Config{MaxPayloadBytes: 10, MetadataPrefix: defaultMetadataPrefix})
	input := makeLogs(body)

	output, err := processor.processLogs(context.Background(), input)

	require.NoError(t, err)
	records := output.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords()
	require.Equal(t, 2, records.Len())
	for i := 0; i < records.Len(); i++ {
		require.LessOrEqual(t, payloadBytes(records.At(i), defaultMetadataPrefix), 10)
	}
	mergedBody := mergeStructuredBody(records)
	require.Equal(t, body.AsRaw(), mergedBody.AsRaw())
	requireStructuredPath(t, records, "/body/message")
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

func requireAttributeBytes(t *testing.T, attrs pcommon.Map, key string, want []byte) {
	t.Helper()
	got, ok := attrs.Get(key)
	require.True(t, ok, "missing attribute %s", key)
	require.Equal(t, pcommon.ValueTypeBytes, got.Type())
	require.Equal(t, want, got.Bytes().AsRaw())
}

func requireAttributeAbsent(t *testing.T, attrs pcommon.Map, key string) {
	t.Helper()
	_, ok := attrs.Get(key)
	require.False(t, ok, "attribute %s should not be present", key)
}

func requireOptionalAttributeStr(t *testing.T, attrs pcommon.Map, key string, want string, wantOK bool) {
	t.Helper()
	got, ok := attrs.Get(key)
	require.Equal(t, wantOK, ok, "attribute %s presence mismatch", key)
	if !wantOK {
		return
	}
	require.Equal(t, want, got.Str())
}

func requireSharesFirstRecord(t *testing.T, input plog.Logs, output plog.Logs) {
	t.Helper()
	firstRecord(output).Attributes().PutStr("shared", "true")
	got, ok := firstRecord(input).Attributes().Get("shared")
	require.True(t, ok, "output should share the original log record")
	require.Equal(t, "true", got.Str())
}

func payloadBytes(record plog.LogRecord, metadataPrefix string) int {
	total := valuePayloadBytes(record.Body())
	if record.Body().Type() == pcommon.ValueTypeEmpty {
		total = 0
	}
	record.Attributes().Range(func(key string, value pcommon.Value) bool {
		if strings.HasPrefix(key, metadataPrefix+".") {
			return true
		}
		total += len(key) + valuePayloadBytes(value)
		return true
	})
	return total
}

func mergeStructuredAttribute(t *testing.T, records plog.LogRecordSlice, key string) pcommon.Value {
	t.Helper()
	merged := pcommon.NewValueEmpty()
	found := false
	for i := 0; i < records.Len(); i++ {
		value, ok := records.At(i).Attributes().Get(key)
		if !ok {
			continue
		}
		found = true
		mergePartialValue(merged, value)
	}
	require.True(t, found, "missing structured attribute %s", key)
	return merged
}

func mergeStructuredBody(records plog.LogRecordSlice) pcommon.Value {
	merged := pcommon.NewValueEmpty()
	for i := 0; i < records.Len(); i++ {
		body := records.At(i).Body()
		if body.Type() != pcommon.ValueTypeMap && body.Type() != pcommon.ValueTypeSlice {
			continue
		}
		mergePartialValue(merged, body)
	}
	return merged
}

func mergePartialValue(destination pcommon.Value, fragment pcommon.Value) {
	switch fragment.Type() {
	case pcommon.ValueTypeEmpty:
		return
	case pcommon.ValueTypeMap:
		if destination.Type() != pcommon.ValueTypeMap {
			destination.SetEmptyMap()
		}
		fragment.Map().Range(func(key string, child pcommon.Value) bool {
			target, ok := destination.Map().Get(key)
			if !ok {
				target = destination.Map().PutEmpty(key)
			}
			mergePartialValue(target, child)
			return true
		})
	case pcommon.ValueTypeSlice:
		if destination.Type() != pcommon.ValueTypeSlice {
			destination.SetEmptySlice()
		}
		target := destination.Slice()
		source := fragment.Slice()
		for target.Len() < source.Len() {
			target.AppendEmpty()
		}
		for i := 0; i < source.Len(); i++ {
			mergePartialValue(target.At(i), source.At(i))
		}
	case pcommon.ValueTypeStr:
		if destination.Type() == pcommon.ValueTypeStr {
			destination.SetStr(destination.Str() + fragment.Str())
			return
		}
		fragment.CopyTo(destination)
	case pcommon.ValueTypeBytes:
		if destination.Type() == pcommon.ValueTypeBytes {
			combined := append([]byte(nil), destination.Bytes().AsRaw()...)
			combined = append(combined, fragment.Bytes().AsRaw()...)
			destination.SetEmptyBytes().FromRaw(combined)
			return
		}
		fragment.CopyTo(destination)
	default:
		fragment.CopyTo(destination)
	}
}

func requireStructuredPath(t *testing.T, records plog.LogRecordSlice, want string) {
	t.Helper()
	require.Greater(t, countStructuredPath(records, want), 0, "missing structured path %q", want)
}

func countStructuredPath(records plog.LogRecordSlice, want string) int {
	key := defaultMetadataPrefix + ".structured_paths"
	count := 0
	for i := 0; i < records.Len(); i++ {
		value, ok := records.At(i).Attributes().Get(key)
		if !ok {
			continue
		}
		paths := value.Slice()
		for j := 0; j < paths.Len(); j++ {
			if paths.At(j).Str() == want {
				count++
			}
		}
	}
	return count
}

func requireNestedContentUTF8(t *testing.T, payload pcommon.Value) {
	t.Helper()
	messages, ok := payload.Map().Get("messages")
	if !ok || messages.Type() != pcommon.ValueTypeSlice || messages.Slice().Len() < 2 {
		return
	}
	message := messages.Slice().At(1)
	if message.Type() != pcommon.ValueTypeMap {
		return
	}
	content, ok := message.Map().Get("content")
	if !ok {
		return
	}
	require.Equal(t, pcommon.ValueTypeStr, content.Type())
	require.True(t, utf8.ValidString(content.Str()))
}

func concatenateBodies(records plog.LogRecordSlice) string {
	var builder strings.Builder
	for i := 0; i < records.Len(); i++ {
		builder.WriteString(records.At(i).Body().Str())
	}
	return builder.String()
}

func concatenateStringAttribute(records plog.LogRecordSlice, key string) string {
	var builder strings.Builder
	for i := 0; i < records.Len(); i++ {
		value, ok := records.At(i).Attributes().Get(key)
		if !ok {
			continue
		}
		builder.WriteString(value.Str())
	}
	return builder.String()
}

func concatenateBytesAttribute(records plog.LogRecordSlice, key string) []byte {
	var out []byte
	for i := 0; i < records.Len(); i++ {
		value, ok := records.At(i).Attributes().Get(key)
		if !ok {
			continue
		}
		out = append(out, value.Bytes().AsRaw()...)
	}
	return out
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
