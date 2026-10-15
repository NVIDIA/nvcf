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
	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-task/internal/types"
	"time"
)

const (
	DefaultEssAgentConfigDir = "/config/ess-agent"
	// EssTokenFileName is the filename the ESS agent writes the assertion JWT to.
	EssTokenFileName             = "jwt.token"
	DefaultSecretsFilePath       = "/var/secrets/secrets.json"
	DefaultHealthPort            = 8080
	DefaultPollProgressInterval  = 5 * time.Second
	DefaultProgressUpdateTimeout = 5 * time.Minute
	DefaultTaskReadyTimeout      = 2 * time.Hour
	MaxTerminationPeriod         = 30 * time.Second
	NgcBaseUrl                   = "https://api.ngc.nvidia.com"
	NgcBaseUrlStg                = "https://api.stg.ngc.nvidia.com"
	DefaultSharedConfigDir       = "/config/shared"
)

type Config struct {
	NVCTWorkerToken          string                       `mapstructure:"NVCT_WORKER_TOKEN"`
	NVCTFqdn                 string                       `mapstructure:"NVCT_FQDN"`
	NVCTFqdnGrpc             string                       `mapstructure:"NVCT_FQDN_GRPC"`
	OTELExporterOTLPEndpoint string                       `mapstructure:"OTEL_EXPORTER_OTLP_ENDPOINT"`
	TaskTags                 []string                     `mapstructure:"TASK_TAGS"`
	TaskId                   string                       `mapstructure:"TASK_ID"`
	TaskName                 string                       `mapstructure:"TASK_NAME"`
	TracingAccessToken       string                       `mapstructure:"TRACING_ACCESS_TOKEN"`
	InstanceId               string                       `mapstructure:"INSTANCE_ID"`
	InstanceType             string                       `mapstructure:"INSTANCE_TYPE"`
	InstanceTypeName         string                       `mapstructure:"INSTANCE_TYPE_NAME"`
	HealthPort               int                          `mapstructure:"HEALTH_PORT"`
	ResultsDir               string                       `mapstructure:"NVCT_RESULTS_DIR"`
	ResultsLocation          string                       `mapstructure:"RESULTS_LOCATION"`
	MaxRunTime               string                       `mapstructure:"NVCT_MAX_RUN_TIME_DURATION"`
	TerminationGracePeriod   string                       `mapstructure:"TERMINATION_GRACE_PERIOD"`
	ResultHandlingStrategy   types.ResultHandlingStrategy `mapstructure:"NVCT_RESULT_HANDLING_STRATEGY"`
	ProgressFilePath         string                       `mapstructure:"NVCT_PROGRESS_FILE_PATH"`
	EssAgentConfigDir        string                       `mapstructure:"ESS_AGENT_CONFIG_DIR"`
	SecretsAssertionToken    string                       `mapstructure:"SECRETS_ASSERTION_TOKEN"`
	NcaId                    string                       `mapstructure:"NCA_ID"`
	BillingNcaId             string                       `mapstructure:"BILLING_NCA_ID"`
	AccountName              string                       `mapstructure:"ACCOUNT_NAME"`
	CloudProvider            string                       `mapstructure:"CLOUD_PROVIDER"`
	CloudPlatform            string                       `mapstructure:"CLOUD_PLATFORM"`
	// SpotEnvironment is deprecated: use ICMSEnvironment (or set ICMS_ENVIRONMENT). If unset, ICMSEnvironment is set from this in setup.
	SpotEnvironment                string        `mapstructure:"SPOT_ENVIRONMENT"`
	ICMSEnvironment                string        `mapstructure:"ICMS_ENVIRONMENT"`
	ZoneName                       string        `mapstructure:"ZONE_NAME"`
	GpuType                        string        `mapstructure:"GPU_NAME"`
	GpuCount                       int           `mapstructure:"ATTACHED_GPU_COUNT"`
	InfraMeteringHeartbeatInterval time.Duration `mapstructure:"INFRA_METERING_HEARTBEAT_INTERVAL_SECS"`
	PollProgress                   bool          `mapstructure:"POLL_PROGRESS"`
	PollProgressInterval           time.Duration `mapstructure:"POLL_PROGRESS_INTERVAL"`
	ProgressUpdateTimeout          time.Duration `mapstructure:"PROGRESS_UPDATE_TIMEOUT"`
	SharedConfigDir                string        `mapstructure:"SHARED_CONFIG_DIR"`
	TaskReadyTimeout               time.Duration `mapstructure:"TASK_READY_TIMEOUT"`
}
