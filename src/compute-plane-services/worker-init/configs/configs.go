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

package configs

import (
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-init/internal/downloader"
)

const (
	DefaultModelsRepo           = "/config/models"
	DefaultResourcesRepo        = "/config/resources"
	DefaultEssAgentConfigDir    = "/config/ess-agent"
	DefaultRawEssAgentConfigDir = "/etc/ess"
	DefaultConcurrentDownloads  = 5
	DefaultConcurrentChunks     = 4
	DefaultChunkSize            = downloader.DefaultChunkSize
	DefaultSharedConfigDir      = "/config/shared"
)

type InitConfig struct {
	BaseConfig `mapstructure:",squash"`
	NvctConfig `mapstructure:",squash"`
	NvcfConfig `mapstructure:",squash"`
}

// Common configs shared by NVCF and NVCT
type BaseConfig struct {
	ConcurrentDownloads int    `mapstructure:"WORKER_CONCURRENT_DOWNLOADS"`
	ConcurrentChunks    int    `mapstructure:"WORKER_CONCURRENT_CHUNKS"`
	ChunkSize           int    `mapstructure:"WORKER_CHUNK_SIZE"`
	ModelRepo           string `mapstructure:"MODEL_REPO"`
	ResourceRepo        string `mapstructure:"RESOURCE_REPO"`
	InstanceId          string `mapstructure:"INSTANCE_ID"`
	InstanceType        string `mapstructure:"INSTANCE_TYPE"`
	InstanceTypeName    string `mapstructure:"INSTANCE_TYPE_NAME"`
	NcaId               string `mapstructure:"NCA_ID"`
	BillingNcaId        string `mapstructure:"BILLING_NCA_ID"`
	AccountName         string `mapstructure:"ACCOUNT_NAME"`
	CloudProvider       string `mapstructure:"CLOUD_PROVIDER"`
	CloudPlatform       string `mapstructure:"CLOUD_PLATFORM"`
	// SpotEnvironment is deprecated: use ICMSEnvironment (or set ICMS_ENVIRONMENT). If unset, ICMSEnvironment is set from this in setup.
	SpotEnvironment                string        `mapstructure:"SPOT_ENVIRONMENT"`
	ICMSEnvironment                string        `mapstructure:"ICMS_ENVIRONMENT"`
	ZoneName                       string        `mapstructure:"ZONE_NAME"`
	GpuType                        string        `mapstructure:"GPU_NAME"`
	GpuCount                       int           `mapstructure:"ATTACHED_GPU_COUNT"`
	InfraMeteringHeartbeatInterval time.Duration `mapstructure:"INFRA_METERING_HEARTBEAT_INTERVAL_SECS"`
	EssAgentConfigDir              string        `mapstructure:"ESS_AGENT_CONFIG_DIR"`
	SecretsAssertionToken          string        `mapstructure:"SECRETS_ASSERTION_TOKEN"`
	OTELExporterOTLPEndpoint       string        `mapstructure:"OTEL_EXPORTER_OTLP_ENDPOINT"`
	TracingAccessToken             string        `mapstructure:"TRACING_ACCESS_TOKEN"`
	SharedConfigDir                string        `mapstructure:"SHARED_CONFIG_DIR"`
}

type NvctConfig struct {
	TaskId          string   `mapstructure:"TASK_ID"`
	TaskName        string   `mapstructure:"TASK_NAME"`
	TaskTags        []string `mapstructure:"TASK_TAGS"`
	NvctFqdnGrpc    string   `mapstructure:"NVCT_FQDN_GRPC"`
	NvctWorkerToken string   `mapstructure:"NVCT_WORKER_TOKEN"`
}

type NvcfConfig struct {
	NvcfFqdnGrpc      string   `mapstructure:"NVCF_FQDN_GRPC"`
	NvcfWorkerToken   string   `mapstructure:"NVCF_WORKER_TOKEN"`
	FunctionId        string   `mapstructure:"FUNCTION_ID"`
	FunctionVersionId string   `mapstructure:"FUNCTION_VERSION_ID"`
	FunctionName      string   `mapstructure:"FUNCTION_NAME"`
	FunctionTags      []string `mapstructure:"FUNCTION_TAGS"` // csv
}
