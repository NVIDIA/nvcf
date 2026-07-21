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

package otelconfig

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/internal/logger"
)

// OTelCollectorConfig configures BYOO OTel collector rendering behavior.
type OTelCollectorConfig struct {
	ExporterHelper ExporterHelperConfig `json:"exporterHelper,omitempty"`
	MemoryLimiter  MemoryLimiterConfig  `json:"memoryLimiter,omitempty"`
	Batch          BatchConfig          `json:"batch,omitempty"`
	LogBatch       BatchConfig          `json:"logBatch,omitempty"`
}

// IsZero returns true when no collector rendering overrides are configured.
func (c OTelCollectorConfig) IsZero() bool {
	return c.ExporterHelper.IsZero() &&
		c.MemoryLimiter.IsZero() &&
		c.Batch.IsZero() &&
		c.LogBatch.IsZero()
}

// ExporterHelperConfig configures common exporterhelper settings for BYOO exporters.
type ExporterHelperConfig struct {
	Timeout        string               `json:"timeout,omitempty"`
	RetryOnFailure RetryOnFailureConfig `json:"retryOnFailure,omitempty"`
	SendingQueue   SendingQueueConfig   `json:"sendingQueue,omitempty"`
}

// IsZero returns true when no exporterhelper overrides are configured.
func (c ExporterHelperConfig) IsZero() bool {
	return c.Timeout == "" && c.RetryOnFailure.IsZero() && c.SendingQueue.IsZero()
}

// RetryOnFailureConfig configures exporterhelper retry_on_failure settings.
type RetryOnFailureConfig struct {
	Enabled         *bool  `json:"enabled,omitempty"`
	InitialInterval string `json:"initialInterval,omitempty"`
	MaxInterval     string `json:"maxInterval,omitempty"`
	MaxElapsedTime  string `json:"maxElapsedTime,omitempty"`
}

// IsZero returns true when no retry_on_failure overrides are configured.
func (c RetryOnFailureConfig) IsZero() bool {
	return c.Enabled == nil && c.InitialInterval == "" && c.MaxInterval == "" && c.MaxElapsedTime == ""
}

// SendingQueueConfig configures exporterhelper sending_queue settings.
type SendingQueueConfig struct {
	Enabled         *bool                   `json:"enabled,omitempty"`
	NumConsumers    *int64                  `json:"numConsumers,omitempty"`
	QueueSize       *int64                  `json:"queueSize,omitempty"`
	Sizer           string                  `json:"sizer,omitempty"`
	Storage         string                  `json:"storage,omitempty"`
	BlockOnOverflow *bool                   `json:"blockOnOverflow,omitempty"`
	WaitForResult   *bool                   `json:"waitForResult,omitempty"`
	Batch           SendingQueueBatchConfig `json:"batch,omitempty"`
}

// IsZero returns true when no sending_queue overrides are configured.
func (c SendingQueueConfig) IsZero() bool {
	return c.Enabled == nil &&
		c.NumConsumers == nil &&
		c.QueueSize == nil &&
		c.Sizer == "" &&
		c.Storage == "" &&
		c.BlockOnOverflow == nil &&
		c.WaitForResult == nil &&
		c.Batch.IsZero()
}

// SendingQueueBatchConfig configures exporterhelper sending_queue.batch settings.
type SendingQueueBatchConfig struct {
	FlushTimeout string `json:"flushTimeout,omitempty"`
	Sizer        string `json:"sizer,omitempty"`
	MinSize      *int64 `json:"minSize,omitempty"`
	MaxSize      *int64 `json:"maxSize,omitempty"`
}

// IsZero returns true when no sending_queue.batch overrides are configured.
func (c SendingQueueBatchConfig) IsZero() bool {
	return c.FlushTimeout == "" && c.Sizer == "" && c.MinSize == nil && c.MaxSize == nil
}

// MemoryLimiterConfig configures the BYOO collector memory_limiter processor.
type MemoryLimiterConfig struct {
	CheckInterval        string `json:"checkInterval,omitempty"`
	LimitMiB             *int64 `json:"limitMiB,omitempty"`
	SpikeLimitMiB        *int64 `json:"spikeLimitMiB,omitempty"`
	LimitPercentage      *int64 `json:"limitPercentage,omitempty"`
	SpikeLimitPercentage *int64 `json:"spikeLimitPercentage,omitempty"`
}

// IsZero returns true when no memory_limiter overrides are configured.
func (c MemoryLimiterConfig) IsZero() bool {
	return c.CheckInterval == "" &&
		c.LimitMiB == nil &&
		c.SpikeLimitMiB == nil &&
		c.LimitPercentage == nil &&
		c.SpikeLimitPercentage == nil
}

// BatchConfig configures the BYOO collector batch processor.
type BatchConfig struct {
	Timeout                  string   `json:"timeout,omitempty"`
	SendBatchSize            *int64   `json:"sendBatchSize,omitempty"`
	SendBatchMaxSize         *int64   `json:"sendBatchMaxSize,omitempty"`
	MetadataKeys             []string `json:"metadataKeys,omitempty"`
	MetadataCardinalityLimit *int64   `json:"metadataCardinalityLimit,omitempty"`
}

// IsZero returns true when no batch processor overrides are configured.
func (c BatchConfig) IsZero() bool {
	return c.Timeout == "" &&
		c.SendBatchSize == nil &&
		c.SendBatchMaxSize == nil &&
		len(c.MetadataKeys) == 0 &&
		c.MetadataCardinalityLimit == nil
}

func decodeOTelCollectorConfig(encoded string) (OTelCollectorConfig, error) {
	if encoded == "" {
		return OTelCollectorConfig{}, nil
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return OTelCollectorConfig{}, fmt.Errorf("decode base64: %w", err)
	}
	var cfg OTelCollectorConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return OTelCollectorConfig{}, fmt.Errorf("decode json: %w", err)
	}
	return cfg, nil
}

func applyOTelCollectorConfig(otelConfig *OpenTelemetryConfig, cfg OTelCollectorConfig) {
	if cfg.IsZero() {
		return
	}
	applyExporterHelperConfig(otelConfig, cfg.ExporterHelper)
	applyMemoryLimiterConfig(otelConfig, cfg.MemoryLimiter)
	applyBatchConfig(otelConfig, "batch", cfg.Batch)
	applyLogBatchConfig(otelConfig, cfg.LogBatch)
}

func applyExporterHelperConfig(otelConfig *OpenTelemetryConfig, cfg ExporterHelperConfig) {
	if cfg.IsZero() {
		return
	}
	for exporterID, exporter := range otelConfig.Exporters {
		if exporterID == "debug" {
			continue
		}
		if cfg.Timeout != "" {
			exporter["timeout"] = cfg.Timeout
		}
		if !cfg.RetryOnFailure.IsZero() {
			retry := mapFromInterface(exporter["retry_on_failure"])
			cfg.RetryOnFailure.applyTo(retry)
			exporter["retry_on_failure"] = retry
		}
		if !cfg.SendingQueue.IsZero() {
			queue := mapFromInterface(exporter["sending_queue"])
			cfg.SendingQueue.applyTo(queue)
			exporter["sending_queue"] = queue
		}
	}
}

func (c RetryOnFailureConfig) applyTo(out map[string]interface{}) {
	if c.Enabled != nil {
		out["enabled"] = *c.Enabled
	}
	if c.InitialInterval != "" {
		out["initial_interval"] = c.InitialInterval
	}
	if c.MaxInterval != "" {
		out["max_interval"] = c.MaxInterval
	}
	if c.MaxElapsedTime != "" {
		out["max_elapsed_time"] = c.MaxElapsedTime
	}
}

func (c SendingQueueConfig) applyTo(out map[string]interface{}) {
	if c.Enabled != nil {
		out["enabled"] = *c.Enabled
	} else if len(out) == 0 {
		out["enabled"] = true
	}
	if c.NumConsumers != nil {
		out["num_consumers"] = *c.NumConsumers
	}
	if c.QueueSize != nil {
		out["queue_size"] = *c.QueueSize
	}
	if c.Sizer != "" {
		out["sizer"] = c.Sizer
	}
	if c.Storage != "" {
		out["storage"] = c.Storage
	}
	if c.BlockOnOverflow != nil {
		out["block_on_overflow"] = *c.BlockOnOverflow
	}
	if c.WaitForResult != nil {
		out["wait_for_result"] = *c.WaitForResult
	}
	if !c.Batch.IsZero() {
		batch := mapFromInterface(out["batch"])
		c.Batch.applyTo(batch)
		out["batch"] = batch
	}
}

func (c SendingQueueBatchConfig) applyTo(out map[string]interface{}) {
	if c.FlushTimeout != "" {
		out["flush_timeout"] = c.FlushTimeout
	}
	if c.Sizer != "" {
		out["sizer"] = c.Sizer
	}
	if c.MinSize != nil {
		out["min_size"] = *c.MinSize
	}
	if c.MaxSize != nil {
		out["max_size"] = *c.MaxSize
	}
}

func applyMemoryLimiterConfig(otelConfig *OpenTelemetryConfig, cfg MemoryLimiterConfig) {
	if cfg.IsZero() {
		return
	}
	if cfg.LimitMiB != nil && cfg.LimitPercentage != nil {
		warnMemoryLimiterConflict("limit_mib", "limit_percentage")
	}
	if cfg.SpikeLimitMiB != nil && cfg.SpikeLimitPercentage != nil {
		warnMemoryLimiterConflict("spike_limit_mib", "spike_limit_percentage")
	}
	processor := mapFromInterface(otelConfig.Processors["memory_limiter"])
	if cfg.CheckInterval != "" {
		processor["check_interval"] = cfg.CheckInterval
	}
	if cfg.LimitMiB != nil {
		processor["limit_mib"] = *cfg.LimitMiB
		delete(processor, "limit_percentage")
	}
	if cfg.SpikeLimitMiB != nil {
		processor["spike_limit_mib"] = *cfg.SpikeLimitMiB
		delete(processor, "spike_limit_percentage")
	}
	if cfg.LimitPercentage != nil {
		processor["limit_percentage"] = *cfg.LimitPercentage
		delete(processor, "limit_mib")
	}
	if cfg.SpikeLimitPercentage != nil {
		processor["spike_limit_percentage"] = *cfg.SpikeLimitPercentage
		delete(processor, "spike_limit_mib")
	}
	otelConfig.Processors["memory_limiter"] = processor
}

func warnMemoryLimiterConflict(mibField, percentageField string) {
	if logger.Logger == nil {
		return
	}
	logger.Logger.Warnf(
		"BYOO OTel collector memory_limiter config sets both %s and %s; using %s and dropping %s",
		mibField,
		percentageField,
		percentageField,
		mibField,
	)
}

func applyBatchConfig(otelConfig *OpenTelemetryConfig, processorID string, cfg BatchConfig) {
	if cfg.IsZero() {
		return
	}
	processor := mapFromInterface(otelConfig.Processors[processorID])
	cfg.applyTo(processor)
	otelConfig.Processors[processorID] = processor
}

func (c BatchConfig) applyTo(processor map[string]interface{}) {
	if c.Timeout != "" {
		processor["timeout"] = c.Timeout
	}
	if c.SendBatchSize != nil {
		processor["send_batch_size"] = *c.SendBatchSize
	}
	if c.SendBatchMaxSize != nil {
		processor["send_batch_max_size"] = *c.SendBatchMaxSize
	}
	if len(c.MetadataKeys) > 0 {
		processor["metadata_keys"] = c.MetadataKeys
	}
	if c.MetadataCardinalityLimit != nil {
		processor["metadata_cardinality_limit"] = *c.MetadataCardinalityLimit
	}
}

func applyLogBatchConfig(otelConfig *OpenTelemetryConfig, cfg BatchConfig) {
	if cfg.IsZero() {
		return
	}
	processor := mapFromInterface(otelConfig.Processors["batch/logs"])
	if len(processor) == 0 {
		if batchProcessor, ok := otelConfig.Processors["batch"]; ok {
			processor = cloneConfigMap(batchProcessor)
		}
	}
	cfg.applyTo(processor)
	otelConfig.Processors["batch/logs"] = processor

	logsPipeline, ok := otelConfig.Service.Pipelines["logs"]
	if !ok {
		return
	}
	replaced := false
	for i, processorID := range logsPipeline.Processors {
		if processorID == "batch" || processorID == "batch/logs" {
			logsPipeline.Processors[i] = "batch/logs"
			replaced = true
		}
	}
	if !replaced {
		logsPipeline.Processors = append(logsPipeline.Processors, "batch/logs")
	}
	otelConfig.Service.Pipelines["logs"] = logsPipeline
}
