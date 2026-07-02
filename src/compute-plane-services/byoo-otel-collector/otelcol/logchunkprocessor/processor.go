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
	"unicode/utf8"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/processor"
	"go.opentelemetry.io/otel/metric/noop"
	"go.uber.org/zap"
)

type chunkIDFunc func(plog.LogRecord, string) (string, error)

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
				_, bodyBytes, ok := p.oversizedBody(records.At(k))
				if !ok {
					continue
				}
				p.metrics.recordOversize(ctx, p.cfg.mode(), int64(bodyBytes))
				p.logger.Warn(
					"oversized log record detected; dry-run mode left body unchanged",
					zap.Int("body_bytes", bodyBytes),
					zap.Int("max_body_bytes", p.cfg.MaxBodyBytes),
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
				if _, _, ok := p.oversizedBody(records.At(k)); ok {
					return true
				}
			}
		}
	}
	return false
}

func (p *logChunkProcessor) oversizedBody(record plog.LogRecord) (string, int, bool) {
	body := record.Body()
	if body.Type() != pcommon.ValueTypeStr {
		return "", 0, false
	}

	bodyStr := body.Str()
	bodyBytes := len(bodyStr)
	return bodyStr, bodyBytes, bodyBytes > p.cfg.MaxBodyBytes
}

func (p *logChunkProcessor) processLogRecords(ctx context.Context, src plog.LogRecordSlice, dst plog.LogRecordSlice) {
	for i := 0; i < src.Len(); i++ {
		record := src.At(i)
		bodyStr, bodyBytes, ok := p.oversizedBody(record)
		if !ok {
			record.CopyTo(dst.AppendEmpty())
			continue
		}

		p.metrics.recordOversize(ctx, p.cfg.mode(), int64(bodyBytes))
		p.chunkRecord(ctx, record, bodyStr, bodyBytes, dst)
	}
}

func (p *logChunkProcessor) chunkRecord(ctx context.Context, record plog.LogRecord, body string, bodyBytes int, dst plog.LogRecordSlice) {
	chunks := splitUTF8ByBytes(body, p.cfg.MaxBodyBytes)
	chunkID, err := p.newChunkID(record, body)
	if err != nil {
		p.metrics.recordError(ctx, p.cfg.mode(), "chunk_id_generation")
		p.logger.Warn("failed to generate random log chunk id; using deterministic fallback", zap.Error(err))
		chunkID = fallbackChunkID(record, body)
	}

	offset := 0
	for i, chunk := range chunks {
		chunkRecord := dst.AppendEmpty()
		record.CopyTo(chunkRecord)
		chunkRecord.Body().SetStr(chunk)
		p.setChunkAttributes(chunkRecord.Attributes(), chunkID, i, len(chunks), offset, bodyBytes)
		offset += len(chunk)
	}

	p.metrics.recordChunks(ctx, p.cfg.mode(), int64(len(chunks)), int64(offset))
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

func splitUTF8ByBytes(s string, limit int) []string {
	if limit <= 0 || len(s) <= limit {
		return []string{s}
	}

	chunks := make([]string, 0, len(s)/limit+1)
	for start := 0; start < len(s); {
		end := start + limit
		if end >= len(s) {
			chunks = append(chunks, s[start:])
			break
		}

		for end > start && !utf8.RuneStart(s[end]) {
			end--
		}
		if end == start {
			_, size := utf8.DecodeRuneInString(s[start:])
			end = start + size
		}

		chunks = append(chunks, s[start:end])
		start = end
	}
	return chunks
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
