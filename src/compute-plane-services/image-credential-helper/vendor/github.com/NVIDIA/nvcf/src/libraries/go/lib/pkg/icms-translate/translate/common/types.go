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

package common

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"unicode"

	"dario.cat/mergo"
)

type MessageAction string

const (
	FunctionCreationAction MessageAction = "RequestICMSInstances"
	TaskCreationAction     MessageAction = "RequestICMSInstancesForTask"
	TerminationAction      MessageAction = "TerminateInstances"

	// Aliases for backward compatibility and test readability
	RequestICMSInstances        = FunctionCreationAction
	RequestICMSInstancesForTask = TaskCreationAction
)

// Normalize converts legacy action values to their ICMS equivalents.
// Returns the normalized action, or the original if no normalization needed.
func (a MessageAction) Normalize() MessageAction {
	s := string(a)
	// Match legacy pattern: "Request" prefix + "Sp" at 7-8 + "Instances" at index 11
	if len(s) >= 20 && strings.HasPrefix(s, "Request") && s[7] == 'S' && s[8] == 'p' && strings.Index(s, "Instances") == 11 {
		if strings.HasSuffix(s, "ForTask") {
			return TaskCreationAction
		}
		return FunctionCreationAction
	}
	return a
}

// UnmarshalJSON implements json.Unmarshaler to support both legacy and new action names.
// Legacy names are normalized to the new ICMS names.
func (a *MessageAction) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	*a = MessageAction(s).Normalize()
	return nil
}

const (
	WorkloadTypeContainer = "container"
	WorkloadTypeHelm      = "helm"
)

const (
	// HelmChartDownloadSecretName is the name of the secret containing username and password
	// to download the referenced helm chart.
	HelmChartDownloadSecretName = "helm-chart-download"
)

// +k8s:deepcopy-gen=true
type CreationQueueMessageMetadata struct {
	RequestID      string        `json:"requestId"`
	Sub            string        `json:"sub"`
	MessageBatchID string        `json:"messageBatchId"`
	NCAID          string        `json:"ncaId"`
	AccountName    string        `json:"accountName"`
	Action         MessageAction `json:"action"`
	InstanceCount  uint64        `json:"instanceCount"`
	// InstanceTypeName is the full instance type name, with GPU number suffix "_{d}x".
	// Use this for metrics/instance identification.
	InstanceTypeName string `json:"instanceTypeName,omitempty"`
	// InstanceTypeValue is the instance type name without GPU number suffix.
	// Use this for label selectors.
	InstanceTypeValue string `json:"instanceTypeValue,omitempty"`
	// Deprecated: use InstanceTypeName.
	InstanceType string `json:"instanceType,omitempty"`

	// OTel fields
	TraceParent string            `json:"traceParent"`
	TraceState  map[string]string `json:"traceState"`

	// These fields will not be set for GFN.
	GPUType           string `json:"gpuType,omitempty"`
	RequestedGPUCount uint64 `json:"requestedGPUCount,omitempty"`

	// DeploymentID is the unique identifier for the deployment (function ID and function version ID can be re-used)
	DeploymentID       string `json:"deploymentId,omitempty"`
	GPUSpecificationID string `json:"gpuSpecificationId,omitempty"`
}

func (m *CreationQueueMessageMetadata) Merge(in CreationQueueMessageMetadata) error {
	return mergo.Merge(m, in)
}

type ResultHandlingStrategy string

const (
	NoHandleResult ResultHandlingStrategy = "NONE"
	UploadResult   ResultHandlingStrategy = "UPLOAD"
)

// +k8s:deepcopy-gen=true
type HelmChartLaunchSpecification struct {
	// HelmChart is the full Helm chart URL.
	HelmChartURL string `json:"helmChart,omitempty"`
	// Values for the Helm release.
	Values []byte `json:"configuration,omitempty"`
}

// Telemetry is the telemetry configuration for a specific protocol.
// Each telemetry config has a protocol (http, grpc), provider (grafana-cloud, datadog, etc.), endpoint (url to send data to), and name (secret name used to send data).
// It is used by the consolidated otelconfig renderer under pkg/otelconfig/config.
type Telemetry struct {
	Protocol string `json:"protocol"`
	Provider string `json:"provider"`
	Endpoint string `json:"endpoint"`
	Name     string `json:"name"`
}

// TelemetriesLaunchSpecification is the launch specification for the telemetry service.
// It is used by the consolidated otelconfig renderer under pkg/otelconfig/config.
// +k8s:deepcopy-gen=false
type TelemetriesLaunchSpecification struct {
	Telemetries struct {
		Logs    *Telemetry `json:"logsTelemetry,omitempty"`
		Metrics *Telemetry `json:"metricsTelemetry,omitempty"`
		Traces  *Telemetry `json:"tracesTelemetry,omitempty"`
	} `json:"telemetries"`
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *TelemetriesLaunchSpecification) DeepCopyInto(out *TelemetriesLaunchSpecification) {
	*out = *in
	if in.Telemetries.Logs != nil {
		out.Telemetries.Logs = &Telemetry{}
		*out.Telemetries.Logs = *in.Telemetries.Logs
	}
	if in.Telemetries.Metrics != nil {
		out.Telemetries.Metrics = &Telemetry{}
		*out.Telemetries.Metrics = *in.Telemetries.Metrics
	}
	if in.Telemetries.Traces != nil {
		out.Telemetries.Traces = &Telemetry{}
		*out.Telemetries.Traces = *in.Telemetries.Traces
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new TelemetriesLaunchSpecification.
func (in *TelemetriesLaunchSpecification) DeepCopy() *TelemetriesLaunchSpecification {
	if in == nil {
		return nil
	}
	out := new(TelemetriesLaunchSpecification)
	in.DeepCopyInto(out)
	return out
}

var (
	_ json.Unmarshaler = (*TelemetriesLaunchSpecification)(nil)
	// For detecting whether data is already JSON (object or array).
	jsonObjectPrefix = []byte("{")
	jsonArrayPrefix  = []byte("[")
)

func (v *TelemetriesLaunchSpecification) UnmarshalJSON(data []byte) error {
	if v == nil || len(data) == 0 {
		return nil
	}
	if !HasJSONPrefix(data) {
		dataB64Str, err := strconv.Unquote(string(data))
		if err != nil {
			dataB64Str = string(data)
		}
		if data, err = base64.StdEncoding.DecodeString(dataB64Str); err != nil {
			return err
		}
	}
	// Use a temporary type to avoid recursion
	type tempSpec struct {
		Telemetries struct {
			Logs    *Telemetry `json:"logsTelemetry,omitempty"`
			Metrics *Telemetry `json:"metricsTelemetry,omitempty"`
			Traces  *Telemetry `json:"tracesTelemetry,omitempty"`
		} `json:"telemetries"`
	}
	var tmp tempSpec
	if err := json.Unmarshal(data, &tmp); err != nil {
		return err
	}
	v.Telemetries = tmp.Telemetries
	return nil
}

func HasJSONPrefix(buf []byte) bool {
	trim := bytes.TrimLeftFunc(buf, unicode.IsSpace)
	return bytes.HasPrefix(trim, jsonObjectPrefix) || bytes.HasPrefix(trim, jsonArrayPrefix)
}

// +k8s:deepcopy-gen=true
type CacheLaunchSpecification struct {
	CacheArtifacts bool   `json:"cacheArtifacts"`
	CacheHandle    string `json:"cacheHandle,omitempty"`
	CacheSize      int64  `json:"cacheSize,omitempty"`
}

// +k8s:deepcopy-gen=true
type RegistryAuthConfig struct {
	K8sSecrets []RegistryAuthSecret `json:"k8sSecrets"`
}

// +k8s:deepcopy-gen=true
type RegistryAuthSecret struct {
	Auths map[string]RegistryAuth `json:"auths"`
}

// +k8s:deepcopy-gen=true
type RegistryAuth struct {
	Auth string `json:"auth"`
}
