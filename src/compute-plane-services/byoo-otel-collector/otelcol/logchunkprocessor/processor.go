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
	"strconv"
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
	key   string
	value pcommon.Value
}

type chunkedField struct {
	body         bool
	key          string
	keyBytes     int
	stringValue  string
	bytesValue   []byte
	value        pcommon.Value
	valueBytes   int
	kind         chunkedFieldKind
	structured   bool
	rootType     pcommon.ValueType
	path         []valuePathElement
	structuredID string
}

type chunkedFieldKind int

const (
	chunkedFieldString chunkedFieldKind = iota
	chunkedFieldBytes
	chunkedFieldValue
)

type valuePathElement struct {
	key     string
	index   int
	isIndex bool
}

type logRecordChunk struct {
	body            string
	bodyValue       pcommon.Value
	hasBodyValue    bool
	stringAttr      map[string]string
	bytesAttr       map[string][]byte
	valueAttr       map[string]pcommon.Value
	structuredPaths []string
	offset          int
	bytes           int
	hasFields       bool
}

type logRecordChunkPlan struct {
	body           string
	bodyBytes      int
	hasBody        bool
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
	bodyBytes := valuePayloadBytes(body)
	if body.Type() != pcommon.ValueTypeEmpty {
		plan.hasBody = true
		plan.body = body.AsString()
		plan.bodyBytes = bodyBytes
		plan.originalBytes += bodyBytes
	}

	record.Attributes().Range(func(key string, value pcommon.Value) bool {
		valueBytes := valuePayloadBytes(value)
		keyBytes := len(key)
		plan.attributes = append(plan.attributes, chunkedAttribute{
			key:   key,
			value: value,
		})
		attributeBytes := keyBytes + valueBytes
		plan.originalBytes += attributeBytes
		plan.attributeBytes += attributeBytes
		return true
	})

	var fields []chunkedField
	if body.Type() == pcommon.ValueTypeStr {
		if bodyBytes > 0 {
			fields = append(fields, valueFields(true, "", body)...)
		}
	} else if body.Type() != pcommon.ValueTypeEmpty {
		fields = append(fields, valueFields(true, "", body)...)
	}

	sort.Slice(plan.attributes, func(i, j int) bool {
		return plan.attributes[i].key < plan.attributes[j].key
	})
	for _, attr := range plan.attributes {
		fields = append(fields, attr.fields()...)
	}

	if plan.originalBytes <= p.cfg.MaxPayloadBytes {
		return logRecordChunkPlan{}, false
	}

	plan.chunks = chunkFields(fields, p.cfg.MaxPayloadBytes)
	return plan, len(plan.chunks) > 0
}

func (attr chunkedAttribute) fields() []chunkedField {
	return valueFields(false, attr.key, attr.value)
}

func valueFields(body bool, key string, value pcommon.Value) []chunkedField {
	fields := make([]chunkedField, 0)
	appendValueFields(&fields, body, key, value, value.Type(), nil)
	return fields
}

// appendValueFields flattens maps and slices in deterministic order. Chunks
// rebuild partial typed containers from the path retained on each leaf.
func appendValueFields(
	fields *[]chunkedField,
	body bool,
	key string,
	value pcommon.Value,
	rootType pcommon.ValueType,
	path []valuePathElement,
) {
	switch value.Type() {
	case pcommon.ValueTypeMap:
		valueMap := value.Map()
		if valueMap.Len() > 0 {
			keys := make([]string, 0, valueMap.Len())
			valueMap.Range(func(childKey string, _ pcommon.Value) bool {
				keys = append(keys, childKey)
				return true
			})
			sort.Strings(keys)
			for _, childKey := range keys {
				child, _ := valueMap.Get(childKey)
				childPath := appendPathElement(path, valuePathElement{key: childKey})
				appendValueFields(fields, body, key, child, rootType, childPath)
			}
			return
		}
	case pcommon.ValueTypeSlice:
		valueSlice := value.Slice()
		if valueSlice.Len() > 0 {
			for i := 0; i < valueSlice.Len(); i++ {
				childPath := appendPathElement(path, valuePathElement{index: i, isIndex: true})
				appendValueFields(fields, body, key, valueSlice.At(i), rootType, childPath)
			}
			return
		}
	}

	field := chunkedField{
		body:       body,
		key:        key,
		keyBytes:   pathPayloadBytes(body, key, path),
		value:      value,
		valueBytes: valuePayloadBytes(value),
		kind:       valueFieldKind(value),
		structured: rootType == pcommon.ValueTypeMap || rootType == pcommon.ValueTypeSlice,
		rootType:   rootType,
		path:       append([]valuePathElement(nil), path...),
	}
	if field.structured {
		field.structuredID = structuredPath(body, key, path)
	}
	switch field.kind {
	case chunkedFieldString:
		field.stringValue = value.Str()
	case chunkedFieldBytes:
		field.bytesValue = value.Bytes().AsRaw()
	}
	*fields = append(*fields, field)
}

func appendPathElement(path []valuePathElement, element valuePathElement) []valuePathElement {
	out := make([]valuePathElement, len(path)+1)
	copy(out, path)
	out[len(path)] = element
	return out
}

func pathPayloadBytes(body bool, key string, path []valuePathElement) int {
	total := 0
	if !body {
		total = len(key)
	}
	for _, element := range path {
		if element.isIndex {
			total += len(strconv.Itoa(element.index))
			continue
		}
		total += len(element.key)
	}
	return total
}

func structuredPath(body bool, key string, path []valuePathElement) string {
	var builder strings.Builder
	if body {
		builder.WriteString("/body")
	} else {
		builder.WriteString("/attributes/")
		builder.WriteString(escapePathToken(key))
	}
	for _, element := range path {
		builder.WriteByte('/')
		if element.isIndex {
			builder.WriteString(strconv.Itoa(element.index))
			continue
		}
		builder.WriteString(escapePathToken(element.key))
	}
	return builder.String()
}

// escapePathToken applies JSON Pointer escaping so consumers can parse
// structured_paths without ambiguity.
func escapePathToken(token string) string {
	token = strings.ReplaceAll(token, "~", "~0")
	return strings.ReplaceAll(token, "/", "~1")
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
		p.setChunkAttributes(attrs, chunkID, i, len(plan.chunks), chunk.offset, plan.originalBytes, chunk.structuredPaths)
	}

	p.metrics.recordChunks(ctx, p.cfg.mode(), int64(len(plan.chunks)), int64(plan.originalBytes))
}

func (p *logChunkProcessor) setChunkAttributes(
	attrs pcommon.Map,
	chunkID string,
	index, count, offset, originalBytes int,
	structuredPaths []string,
) {
	prefix := p.cfg.MetadataPrefix
	attrs.PutStr(prefix+".id", chunkID)
	attrs.PutInt(prefix+".index", int64(index))
	attrs.PutInt(prefix+".count", int64(count))
	attrs.PutInt(prefix+".offset_bytes", int64(offset))
	attrs.PutInt(prefix+".original_size_bytes", int64(originalBytes))
	attrs.PutBool(prefix+".final", index == count-1)
	if len(structuredPaths) > 0 {
		paths := attrs.PutEmptySlice(prefix + ".structured_paths")
		for _, path := range structuredPaths {
			paths.AppendEmpty().SetStr(path)
		}
	}
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
		appendValueFingerprint(&builder, attr.value)
	}
	return builder.String()
}

func appendValueFingerprint(builder *strings.Builder, value pcommon.Value) {
	builder.WriteString(strconv.Itoa(int(value.Type())))
	builder.WriteByte(':')
	switch value.Type() {
	case pcommon.ValueTypeMap:
		valueMap := value.Map()
		keys := make([]string, 0, valueMap.Len())
		valueMap.Range(func(key string, _ pcommon.Value) bool {
			keys = append(keys, key)
			return true
		})
		sort.Strings(keys)
		for _, key := range keys {
			child, _ := valueMap.Get(key)
			builder.WriteString(strconv.Itoa(len(key)))
			builder.WriteByte(':')
			builder.WriteString(key)
			appendValueFingerprint(builder, child)
		}
	case pcommon.ValueTypeSlice:
		valueSlice := value.Slice()
		for i := 0; i < valueSlice.Len(); i++ {
			appendValueFingerprint(builder, valueSlice.At(i))
		}
	case pcommon.ValueTypeBytes:
		builder.Write(value.Bytes().AsRaw())
	default:
		builder.WriteString(value.AsString())
	}
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
				if field.body && !field.structured {
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
			if field.body && !field.structured {
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
	if field.structured {
		chunk.addStructuredValue(field, pcommon.NewValueStr(part))
	} else if field.body {
		chunk.body += part
	} else {
		chunk.stringAttr[field.key] += part
	}
	chunk.bytes += field.keyBytes + len(part)
	chunk.hasFields = true
}

func (chunk *logRecordChunk) addBytes(field chunkedField, part []byte) {
	if field.structured {
		value := pcommon.NewValueBytes()
		value.Bytes().FromRaw(part)
		chunk.addStructuredValue(field, value)
		chunk.bytes += field.keyBytes + len(part)
		chunk.hasFields = true
		return
	}
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
	if field.structured {
		chunk.addStructuredValue(field, field.value)
		chunk.bytes += field.fullBytes()
		chunk.hasFields = true
		return
	}
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

func (chunk *logRecordChunk) addStructuredValue(field chunkedField, value pcommon.Value) {
	var root pcommon.Value
	if field.body {
		if !chunk.hasBodyValue {
			chunk.bodyValue = newStructuredRoot(field.rootType)
			chunk.hasBodyValue = true
		}
		root = chunk.bodyValue
	} else {
		var ok bool
		root, ok = chunk.valueAttr[field.key]
		if !ok {
			root = newStructuredRoot(field.rootType)
			chunk.valueAttr[field.key] = root
		}
	}
	copyValueAtPath(root, field.path, value)
	for _, path := range chunk.structuredPaths {
		if path == field.structuredID {
			return
		}
	}
	chunk.structuredPaths = append(chunk.structuredPaths, field.structuredID)
}

func newStructuredRoot(rootType pcommon.ValueType) pcommon.Value {
	if rootType == pcommon.ValueTypeSlice {
		return pcommon.NewValueSlice()
	}
	return pcommon.NewValueMap()
}

func copyValueAtPath(destination pcommon.Value, path []valuePathElement, value pcommon.Value) {
	if len(path) == 0 {
		value.CopyTo(destination)
		return
	}

	element := path[0]
	var child pcommon.Value
	if element.isIndex {
		if destination.Type() != pcommon.ValueTypeSlice {
			destination.SetEmptySlice()
		}
		valueSlice := destination.Slice()
		for valueSlice.Len() <= element.index {
			valueSlice.AppendEmpty()
		}
		child = valueSlice.At(element.index)
	} else {
		if destination.Type() != pcommon.ValueTypeMap {
			destination.SetEmptyMap()
		}
		valueMap := destination.Map()
		var ok bool
		child, ok = valueMap.Get(element.key)
		if !ok {
			child = valueMap.PutEmpty(element.key)
		}
	}
	copyValueAtPath(child, path[1:], value)
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
	switch value.Type() {
	case pcommon.ValueTypeStr:
		return len(value.Str())
	case pcommon.ValueTypeBytes:
		return len(value.Bytes().AsRaw())
	case pcommon.ValueTypeMap:
		total := 0
		value.Map().Range(func(key string, child pcommon.Value) bool {
			total += len(key) + valuePayloadBytes(child)
			return true
		})
		return total
	case pcommon.ValueTypeSlice:
		total := 0
		valueSlice := value.Slice()
		for i := 0; i < valueSlice.Len(); i++ {
			total += valuePayloadBytes(valueSlice.At(i))
		}
		return total
	default:
		return len(value.AsString())
	}
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
