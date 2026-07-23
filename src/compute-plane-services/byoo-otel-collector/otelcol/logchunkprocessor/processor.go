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
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/processor"
	"go.opentelemetry.io/otel/metric/noop"
	"go.uber.org/zap"
)

type chunkIDFunc func(plog.LogRecord, string) (string, error)

type chunkedAttribute struct {
	key          string
	value        pcommon.Value
	keyBytes     int
	payloadBytes int
	kind         chunkedFieldKind
}

type chunkedField struct {
	body        bool
	key         string
	keyBytes    int
	stringValue string
	bytesValue  []byte
	value       pcommon.Value
	valueBytes  int
	kind        chunkedFieldKind
}

type chunkedFieldKind int

const (
	chunkedFieldString chunkedFieldKind = iota
	chunkedFieldBytes
	chunkedFieldValue
)

type logRecordChunk struct {
	body         string
	bodyValue    pcommon.Value
	hasBodyValue bool
	stringAttr   map[string]string
	bytesAttr    map[string][]byte
	valueAttr    map[string]pcommon.Value
	offset       int
	bytes        int
	hasFields    bool
}

type logRecordChunkPlan struct {
	body           string
	bodyBytes      int
	hasBody        bool
	bodyKind       chunkedFieldKind
	attributes     []chunkedAttribute
	chunks         []logRecordChunk
	originalBytes  int
	attributeBytes int
}

type logChunkProcessor struct {
	cfg        Config
	logger     *zap.Logger
	metrics    processorMetrics
	newChunkID chunkIDFunc
}

func newProcessor(set processor.Settings, cfg *Config) (*logChunkProcessor, error) {
	normalized := cfg.normalized()
	if err := normalized.Validate(); err != nil {
		return nil, err
	}

	meterProvider := set.MeterProvider
	if meterProvider == nil {
		meterProvider = noop.NewMeterProvider()
	}
	metrics, err := newProcessorMetrics(meterProvider.Meter("github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/otelcol/logchunkprocessor"))
	if err != nil {
		return nil, err
	}

	logger := set.Logger
	if logger == nil {
		logger = zap.NewNop()
	}

	return &logChunkProcessor{
		cfg:        normalized,
		logger:     logger,
		metrics:    metrics,
		newChunkID: randomChunkID,
	}, nil
}

func (p *logChunkProcessor) processLogs(ctx context.Context, logs plog.Logs) (plog.Logs, error) {
	if !p.cfg.enabled() {
		return logs, nil
	}
	if p.cfg.DryRun {
		p.observeOversizedLogRecords(ctx, logs)
		return logs, nil
	}
	if !p.hasOversizedLogRecord(logs) {
		return logs, nil
	}

	out := plog.NewLogs()
	for i := 0; i < logs.ResourceLogs().Len(); i++ {
		srcResourceLogs := logs.ResourceLogs().At(i)
		dstResourceLogs := out.ResourceLogs().AppendEmpty()
		srcResourceLogs.Resource().CopyTo(dstResourceLogs.Resource())
		dstResourceLogs.SetSchemaUrl(srcResourceLogs.SchemaUrl())

		for j := 0; j < srcResourceLogs.ScopeLogs().Len(); j++ {
			srcScopeLogs := srcResourceLogs.ScopeLogs().At(j)
			dstScopeLogs := dstResourceLogs.ScopeLogs().AppendEmpty()
			srcScopeLogs.Scope().CopyTo(dstScopeLogs.Scope())
			dstScopeLogs.SetSchemaUrl(srcScopeLogs.SchemaUrl())

			p.processLogRecords(ctx, srcScopeLogs.LogRecords(), dstScopeLogs.LogRecords())
		}
	}

	return out, nil
}

func (p *logChunkProcessor) observeOversizedLogRecords(ctx context.Context, logs plog.Logs) {
	for i := 0; i < logs.ResourceLogs().Len(); i++ {
		resourceLogs := logs.ResourceLogs().At(i)
		for j := 0; j < resourceLogs.ScopeLogs().Len(); j++ {
			records := resourceLogs.ScopeLogs().At(j).LogRecords()
			for k := 0; k < records.Len(); k++ {
				plan, ok := p.chunkPlan(records.At(k))
				if !ok {
					continue
				}
				p.metrics.recordOversize(ctx, p.cfg.mode(), int64(plan.originalBytes))
				p.logger.Warn(
					"oversized log record detected; dry-run mode left fields unchanged",
					zap.Int("payload_bytes", plan.originalBytes),
					zap.Int("body_bytes", plan.bodyBytes),
					zap.Int("attribute_payload_bytes", plan.attributeBytes),
					zap.Int("chunked_attribute_count", len(plan.attributes)),
					zap.Int("max_payload_bytes", p.cfg.MaxPayloadBytes),
				)
			}
		}
	}
}

func (p *logChunkProcessor) hasOversizedLogRecord(logs plog.Logs) bool {
	for i := 0; i < logs.ResourceLogs().Len(); i++ {
		resourceLogs := logs.ResourceLogs().At(i)
		for j := 0; j < resourceLogs.ScopeLogs().Len(); j++ {
			records := resourceLogs.ScopeLogs().At(j).LogRecords()
			for k := 0; k < records.Len(); k++ {
				if _, ok := p.chunkPlan(records.At(k)); ok {
					return true
				}
			}
		}
	}
	return false
}

func (p *logChunkProcessor) chunkPlan(record plog.LogRecord) (logRecordChunkPlan, bool) {
	var plan logRecordChunkPlan

	body := record.Body()
	bodyKind := valueFieldKind(body)
	bodyBytes := valuePayloadBytes(body)
	if body.Type() == pcommon.ValueTypeStr {
		bodyStr := body.Str()
		plan.hasBody = true
		plan.body = bodyStr
		plan.bodyKind = chunkedFieldString
		plan.bodyBytes = bodyBytes
		plan.originalBytes += bodyBytes
	}

	record.Attributes().Range(func(key string, value pcommon.Value) bool {
		valueBytes := valuePayloadBytes(value)
		keyBytes := len(key)
		plan.attributes = append(plan.attributes, chunkedAttribute{
			key:          key,
			value:        value,
			keyBytes:     keyBytes,
			payloadBytes: valueBytes,
			kind:         valueFieldKind(value),
		})
		attributeBytes := keyBytes + valueBytes
		plan.originalBytes += attributeBytes
		plan.attributeBytes += attributeBytes
		return true
	})

	var fields []chunkedField
	if body.Type() == pcommon.ValueTypeStr {
		if bodyBytes > 0 {
			fields = append(fields, chunkedField{
				body:        true,
				stringValue: body.Str(),
				valueBytes:  bodyBytes,
				kind:        chunkedFieldString,
			})
		}
	} else if len(plan.attributes) > 0 && body.Type() != pcommon.ValueTypeEmpty {
		plan.hasBody = true
		plan.body = body.AsString()
		plan.bodyKind = bodyKind
		plan.bodyBytes = bodyBytes
		plan.originalBytes += bodyBytes
		fields = append(fields, chunkedField{
			body:       true,
			value:      body,
			valueBytes: bodyBytes,
			kind:       bodyKind,
		})
	}

	sort.Slice(plan.attributes, func(i, j int) bool {
		return plan.attributes[i].key < plan.attributes[j].key
	})
	for _, attr := range plan.attributes {
		fields = append(fields, attr.field())
	}

	if plan.originalBytes <= p.cfg.MaxPayloadBytes {
		return logRecordChunkPlan{}, false
	}

	plan.chunks = chunkFields(fields, p.cfg.MaxPayloadBytes)
	return plan, len(plan.chunks) > 0
}

func (attr chunkedAttribute) field() chunkedField {
	field := chunkedField{
		key:        attr.key,
		keyBytes:   attr.keyBytes,
		value:      attr.value,
		valueBytes: attr.payloadBytes,
		kind:       attr.kind,
	}
	switch attr.kind {
	case chunkedFieldString:
		field.stringValue = attr.value.Str()
	case chunkedFieldBytes:
		field.bytesValue = attr.value.Bytes().AsRaw()
	}
	return field
}

func (p *logChunkProcessor) processLogRecords(ctx context.Context, src plog.LogRecordSlice, dst plog.LogRecordSlice) {
	for i := 0; i < src.Len(); i++ {
		record := src.At(i)
		plan, ok := p.chunkPlan(record)
		if !ok {
			record.CopyTo(dst.AppendEmpty())
			continue
		}

		p.metrics.recordOversize(ctx, p.cfg.mode(), int64(plan.originalBytes))
		p.chunkRecord(ctx, record, plan, dst)
	}
}

func (p *logChunkProcessor) chunkRecord(ctx context.Context, record plog.LogRecord, plan logRecordChunkPlan, dst plog.LogRecordSlice) {
	chunkID, err := p.newChunkID(record, plan.chunkIDSource())
	if err != nil {
		p.metrics.recordError(ctx, p.cfg.mode(), "chunk_id_generation")
		p.logger.Warn("failed to generate random log chunk id; using deterministic fallback", zap.Error(err))
		chunkID = fallbackChunkID(record, plan.chunkIDSource())
	}

	for i, chunk := range plan.chunks {
		chunkRecord := dst.AppendEmpty()
		record.CopyTo(chunkRecord)
		if plan.hasBody {
			if chunk.hasBodyValue {
				chunk.bodyValue.CopyTo(chunkRecord.Body())
			} else {
				chunkRecord.Body().SetStr(chunk.body)
			}
		}

		attrs := chunkRecord.Attributes()
		for _, attr := range plan.attributes {
			attrs.Remove(attr.key)
		}
		for key, value := range chunk.stringAttr {
			attrs.PutStr(key, value)
		}
		for key, value := range chunk.bytesAttr {
			attrs.PutEmptyBytes(key).FromRaw(value)
		}
		for key, value := range chunk.valueAttr {
			value.CopyTo(attrs.PutEmpty(key))
		}
		p.setChunkAttributes(attrs, chunkID, i, len(plan.chunks), chunk.offset, plan.originalBytes)
	}

	p.metrics.recordChunks(ctx, p.cfg.mode(), int64(len(plan.chunks)), int64(plan.originalBytes))
}

func (p *logChunkProcessor) setChunkAttributes(attrs pcommon.Map, chunkID string, index, count, offset, originalBytes int) {
	prefix := p.cfg.MetadataPrefix
	attrs.PutStr(prefix+".id", chunkID)
	attrs.PutInt(prefix+".index", int64(index))
	attrs.PutInt(prefix+".count", int64(count))
	attrs.PutInt(prefix+".offset_bytes", int64(offset))
	attrs.PutInt(prefix+".original_size_bytes", int64(originalBytes))
	attrs.PutBool(prefix+".final", index == count-1)
}

func (plan logRecordChunkPlan) chunkIDSource() string {
	if len(plan.attributes) == 0 {
		return plan.body
	}

	var builder strings.Builder
	builder.WriteString(plan.body)
	for _, attr := range plan.attributes {
		builder.WriteByte('\n')
		builder.WriteString(attr.key)
		builder.WriteByte('=')
		builder.WriteString(attr.value.AsString())
	}
	return builder.String()
}

func chunkFields(fields []chunkedField, limit int) []logRecordChunk {
	if limit <= 0 {
		return []logRecordChunk{newLogRecordChunk(0)}
	}

	chunks := make([]logRecordChunk, 0)
	current := newLogRecordChunk(0)
	remaining := limit
	offset := 0

	for _, field := range fields {
		if field.valueBytes == 0 {
			if current.hasFields && field.fullBytes() > remaining {
				chunks = append(chunks, current)
				current = newLogRecordChunk(offset)
				remaining = limit
			}
			current.addEmpty(field)
			offset += field.fullBytes()
			if field.fullBytes() >= remaining {
				chunks = append(chunks, current)
				current = newLogRecordChunk(offset)
				remaining = limit
			} else {
				remaining -= field.fullBytes()
			}
			continue
		}

		if field.kind == chunkedFieldValue {
			if current.hasFields && field.fullBytes() > remaining {
				chunks = append(chunks, current)
				current = newLogRecordChunk(offset)
				remaining = limit
			}
			current.addValue(field)
			offset += field.fullBytes()
			if field.fullBytes() >= remaining {
				chunks = append(chunks, current)
				current = newLogRecordChunk(offset)
				remaining = limit
			} else {
				remaining -= field.fullBytes()
			}
			continue
		}

		if field.kind == chunkedFieldBytes {
			value := field.bytesValue
			for len(value) > 0 {
				partBudget := remaining - field.keyBytes
				if field.body {
					partBudget = remaining
				}
				if partBudget <= 0 && current.hasFields {
					chunks = append(chunks, current)
					current = newLogRecordChunk(offset)
					remaining = limit
					continue
				}

				part, rest := splitBytesPrefix(value, partBudget)
				current.addBytes(field, part)
				used := field.keyBytes + len(part)
				offset += used
				value = rest
				if used >= remaining {
					chunks = append(chunks, current)
					current = newLogRecordChunk(offset)
					remaining = limit
				} else {
					remaining -= used
				}
			}
			continue
		}

		value := field.stringValue
		for len(value) > 0 {
			if remaining == 0 {
				chunks = append(chunks, current)
				current = newLogRecordChunk(offset)
				remaining = limit
			}

			partBudget := remaining - field.keyBytes
			if field.body {
				partBudget = remaining
			}
			if partBudget <= 0 && current.hasFields {
				chunks = append(chunks, current)
				current = newLogRecordChunk(offset)
				remaining = limit
				continue
			}

			part, rest := splitUTF8PrefixByBytes(value, partBudget)
			if field.keyBytes+len(part) > remaining && current.hasFields {
				chunks = append(chunks, current)
				current = newLogRecordChunk(offset)
				remaining = limit
				continue
			}
			if part == "" {
				if current.hasFields {
					chunks = append(chunks, current)
				}
				current = newLogRecordChunk(offset)
				remaining = limit
				continue
			}

			current.addString(field, part)
			used := field.keyBytes + len(part)
			offset += used
			value = rest
			if used >= remaining {
				chunks = append(chunks, current)
				current = newLogRecordChunk(offset)
				remaining = limit
			} else {
				remaining -= used
			}
		}
	}

	if current.hasFields {
		chunks = append(chunks, current)
	}
	return chunks
}

func newLogRecordChunk(offset int) logRecordChunk {
	return logRecordChunk{
		stringAttr: map[string]string{},
		bytesAttr:  map[string][]byte{},
		valueAttr:  map[string]pcommon.Value{},
		offset:     offset,
	}
}

func (chunk *logRecordChunk) addEmpty(field chunkedField) {
	switch field.kind {
	case chunkedFieldString:
		chunk.addString(field, "")
	case chunkedFieldBytes:
		chunk.addBytes(field, nil)
	default:
		chunk.addValue(field)
	}
}

func (chunk *logRecordChunk) addString(field chunkedField, part string) {
	if field.body {
		chunk.body += part
	} else {
		chunk.stringAttr[field.key] += part
	}
	chunk.bytes += field.keyBytes + len(part)
	chunk.hasFields = true
}

func (chunk *logRecordChunk) addBytes(field chunkedField, part []byte) {
	if field.body {
		chunk.bodyValue = pcommon.NewValueBytes()
		chunk.bodyValue.Bytes().FromRaw(part)
		chunk.hasBodyValue = true
		chunk.bytes += len(part)
		chunk.hasFields = true
		return
	}
	chunk.bytesAttr[field.key] = append(chunk.bytesAttr[field.key], part...)
	chunk.bytes += field.keyBytes + len(part)
	chunk.hasFields = true
}

func (chunk *logRecordChunk) addValue(field chunkedField) {
	if field.body {
		chunk.bodyValue = field.value
		chunk.hasBodyValue = true
		chunk.bytes += field.valueBytes
		chunk.hasFields = true
		return
	}
	chunk.valueAttr[field.key] = field.value
	chunk.bytes += field.fullBytes()
	chunk.hasFields = true
}

func (field chunkedField) fullBytes() int {
	return field.keyBytes + field.valueBytes
}

func valueFieldKind(value pcommon.Value) chunkedFieldKind {
	if value.Type() == pcommon.ValueTypeStr {
		return chunkedFieldString
	}
	if value.Type() == pcommon.ValueTypeBytes {
		return chunkedFieldBytes
	}
	return chunkedFieldValue
}

func valuePayloadBytes(value pcommon.Value) int {
	if value.Type() == pcommon.ValueTypeStr {
		return len(value.Str())
	}
	if value.Type() == pcommon.ValueTypeBytes {
		return len(value.Bytes().AsRaw())
	}
	return len(value.AsString())
}

func splitBytesPrefix(bytes []byte, limit int) ([]byte, []byte) {
	if limit <= 0 || len(bytes) <= limit {
		return bytes, nil
	}
	return bytes[:limit], bytes[limit:]
}

func splitUTF8ByBytes(s string, limit int) []string {
	if limit <= 0 || len(s) <= limit {
		return []string{s}
	}

	chunks := make([]string, 0, len(s)/limit+1)
	for len(s) > 0 {
		var chunk string
		chunk, s = splitUTF8PrefixByBytes(s, limit)
		chunks = append(chunks, chunk)
	}
	return chunks
}

func splitUTF8PrefixByBytes(s string, limit int) (string, string) {
	if limit <= 0 || len(s) <= limit {
		return s, ""
	}

	end := limit
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	if end == 0 {
		_, size := utf8.DecodeRuneInString(s)
		end = size
	}
	return s[:end], s[end:]
}

func randomChunkID(_ plog.LogRecord, _ string) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func fallbackChunkID(record plog.LogRecord, body string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf(
		"%s|%s|%d|%d|%s",
		record.TraceID().String(),
		record.SpanID().String(),
		record.Timestamp(),
		record.ObservedTimestamp(),
		body,
	)))
	return hex.EncodeToString(sum[:16])
}
