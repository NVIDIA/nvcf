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
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/kelseyhightower/envconfig"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/internal/logger"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/internal/metrics"
)

type envConfig struct {
	NvcfBackendType             string `split_words:"true" required:"true"`
	NvcfInstanceID              string `split_words:"true" required:"true"`
	NvcfNamespace               string `split_words:"true" required:"true"`
	NvcfWorkloadType            string `split_words:"true" required:"true"`
	NvcfFunctionID              string `split_words:"true"`
	NvcfFunctionVersionID       string `split_words:"true"`
	NvctTaskID                  string `split_words:"true"`
	NvcfZoneName                string `split_words:"true"`
	ByooLogChunkingEnabled      bool   `split_words:"true"`
	ByooLogChunkMaxPayloadBytes int    `split_words:"true"`
	// Deprecated: use ByooLogChunkMaxPayloadBytes.
	ByooLogChunkMaxBodyBytes      int    `split_words:"true"`
	ByooLogChunkDryRun            bool   `split_words:"true"`
	ByooDebugMode                 bool   `split_words:"true"`
	ByooMetricSubsetEnabled       bool   `split_words:"true"`
	ByooMetricSubsetFilterConfig  string `split_words:"true"`
	ByooWorkloadMetricsDropLabels string `split_words:"true"`
	ByooOtelCollectorConfigB64    string `split_words:"true"`
}

func processEnvConfig(env *envConfig) error {
	return envconfig.Process("", env)
}

func getTemplateConfig() (TemplateConfig, error) {
	var env envConfig
	if err := processEnvConfig(&env); err != nil {
		return TemplateConfig{}, fmt.Errorf("error loading environment variables: %w", err)
	}

	var tcgf TemplateConfig

	backendType := env.NvcfBackendType
	switch backendType {
	case "gfn":
		tcgf.BackendType = BackendType("vm")
		tcgf.ZoneName = env.NvcfZoneName
	case "non-gfn":
		tcgf.BackendType = BackendType("k8s")
		tcgf.ZoneName = os.Getenv("NVCF_CLUSTER_REGION")
	}

	tcgf.WorkloadType = WorkloadType(env.NvcfWorkloadType)
	tcgf.Namespace = env.NvcfNamespace
	tcgf.InstanceID = env.NvcfInstanceID
	tcgf.LogChunking.Enabled = env.ByooLogChunkingEnabled
	tcgf.LogChunking.MaxPayloadBytes = env.ByooLogChunkMaxPayloadBytes
	tcgf.LogChunking.MaxBodyBytes = env.ByooLogChunkMaxBodyBytes
	tcgf.LogChunking.DryRun = env.ByooLogChunkDryRun
	logChunking, err := resolvedLogChunkingConfig(tcgf.LogChunking)
	if err != nil {
		return TemplateConfig{}, fmt.Errorf("BYOO_LOG_CHUNK_MAX_PAYLOAD_BYTES: %w", err)
	}
	tcgf.LogChunking = logChunking
	otelCollectorConfig, err := decodeOTelCollectorConfig(env.ByooOtelCollectorConfigB64)
	if err != nil {
		return TemplateConfig{}, fmt.Errorf("BYOO_OTEL_COLLECTOR_CONFIG_B64: %w", err)
	}
	tcgf.OTelCollector = otelCollectorConfig
	tcgf.DebugMode = env.ByooDebugMode
	metricSubsetFilterConfig, err := resolvedMetricSubsetFilterConfig(env.ByooMetricSubsetFilterConfig)
	if err != nil {
		return TemplateConfig{}, fmt.Errorf("BYOO_METRIC_SUBSET_FILTER_CONFIG: %w", err)
	}
	tcgf.MetricSubset = MetricSubsetConfig{
		Enabled:      env.ByooMetricSubsetEnabled,
		FilterConfig: metricSubsetFilterConfig,
	}
	tcgf.WorkloadMetrics.DropLabels = resolvedWorkloadMetricsDropLabels(env.ByooWorkloadMetricsDropLabels, env.ByooMetricSubsetEnabled)

	functionID := env.NvcfFunctionID
	functionVersionID := env.NvcfFunctionVersionID
	taskID := env.NvctTaskID
	if taskID == "unknown" || taskID == "" {
		if functionID == "unknown" || functionID == "" || functionVersionID == "unknown" || functionVersionID == "" {
			return TemplateConfig{}, fmt.Errorf("error: either NVCT_TASK_ID must be specified, or both NVCF_FUNCTION_ID and NVCF_FUNCTION_VERSION_ID must be specified")
		}
		tcgf.FunctionID = functionID
		tcgf.FunctionVersionID = functionVersionID
	} else {
		if functionID != "" || functionVersionID != "" {
			return TemplateConfig{}, fmt.Errorf("error: if NVCT_TASK_ID is specified, NVCF_FUNCTION_ID and NVCF_FUNCTION_VERSION_ID must not be specified")
		}
		tcgf.TaskID = taskID
	}

	return tcgf, nil
}

// generateConfig generates the BYOO Otel Collector configuration file based on the provided template config and telemetries
func GenerateConfig(output, telemetries string) error {
	start := time.Now()
	status := metrics.StatusSuccess

	defer func() {
		duration := time.Since(start)
		metrics.RecordOperationDuration(metrics.GenerateConfig, status, duration)
		metrics.IncrementOperationStatus(metrics.GenerateConfig, status)
	}()

	tcgf, err := getTemplateConfig()
	if err != nil {
		status = metrics.StatusError
		return fmt.Errorf("error getting template config: %w", err)
	}
	outputDir := filepath.Dir(output)
	configFile := filepath.Base(output)

	logger.Logger.Infof("BYOO TemplateConfig: %+v", tcgf)
	logger.Logger.Infof("BYOO Telemetries: %s", telemetries)

	decodedByooData, err := base64.StdEncoding.DecodeString(telemetries)
	if err != nil {
		status = metrics.StatusError
		return fmt.Errorf("error decoding telemetries: %w", err)
	}

	data, err := RenderOtelConfigFromBytes(decodedByooData, tcgf)
	if err != nil {
		status = metrics.StatusError
		return fmt.Errorf("error rendering byoo-otel-collector config: %w", err)
	}

	// Encode the config in base64 for logging
	encodedConfig := base64.StdEncoding.EncodeToString(data)
	logger.Logger.Infof("BYOO Otel Collector config (base64): %s", encodedConfig)

	err = os.WriteFile(filepath.Join(outputDir, configFile), data, 0644)
	if err != nil {
		status = metrics.StatusError
		return fmt.Errorf("error writing config file: %w", err)
	}

	return nil
}
