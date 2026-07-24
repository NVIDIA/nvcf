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

// Package spec builds synthetic NVCF function launch specifications and the
// NVCA-equivalent translate configuration used to drive the shared SIS
// translation library. Feeding these through function.Translate yields the
// exact workload shape NVCA produces in production, including BYOO collector
// placement.
package spec

import (
	"encoding/base64"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
)

// Shape is a supported NVCF function deployment shape.
type Shape string

const (
	// ShapeContainer is a container function; the BYOO collector is a workload
	// sidecar on the inference pod.
	ShapeContainer Shape = "container"
	// ShapeHelm is a Helm function; the BYOO collector runs on the generated
	// utils pod and is fronted by a ClusterIP Service.
	ShapeHelm Shape = "helm"
)

// Default image and identifier values for synthetic specs. The collector image
// tag must be greater than the icms-translate v2 cutoff (0.119.4) so the
// rendered collector uses the runtime-config ("v2") code path that matches
// current production.
const (
	DefaultCollectorImage = "nvcr.io/nvidia/nvcf-core/byoo-otel-collector:0.153.1"
	DefaultInferenceImage = "nvcr.io/nvidia/nvcf-perf/stub-inference:latest"
	DefaultUtilsImage     = "nvcr.io/nvidia/nvcf-core/worker-utils:latest"
	DefaultInitImage      = "nvcr.io/nvidia/nvcf-core/worker-init:latest"
	DefaultHelmChartURL   = "https://helm.example.invalid/charts/perf-test-chart-0.1.0.tgz"

	// InstanceTypeLabelKey mirrors the label selector key NVCA uses for
	// instance-type node affinity.
	InstanceTypeLabelKey = "node.kubernetes.io/instance-type"

	instanceTypeValue = "perf.gpu"
)

// Options controls the synthetic launch specification and translate config.
type Options struct {
	Namespace      string
	ObjectNameBase string

	CollectorImage string
	InferenceImage string
	UtilsImage     string
	InitImage      string

	ClusterRegion string
	ClusterName   string

	// Telemetry destination knobs. These are placeholder endpoints for
	// rendering; deployment (S5/S6) overrides them to point at the in-cluster
	// OTLP sink.
	Protocol        string
	Provider        string
	LogsEndpoint    string
	MetricsEndpoint string
	EnableLogs      bool
	EnableMetrics   bool
}

// DefaultOptions returns a self-consistent set of options for local rendering.
func DefaultOptions() Options {
	return Options{
		Namespace:       "byoo-perf",
		ObjectNameBase:  "perf",
		CollectorImage:  DefaultCollectorImage,
		InferenceImage:  DefaultInferenceImage,
		UtilsImage:      DefaultUtilsImage,
		InitImage:       DefaultInitImage,
		ClusterRegion:   "perf-local",
		ClusterName:     "perf",
		Protocol:        "grpc",
		Provider:        "GRAFANA_CLOUD",
		LogsEndpoint:    "https://logs.perf.invalid/otlp",
		MetricsEndpoint: "https://metrics.perf.invalid/otlp",
		EnableLogs:      true,
		EnableMetrics:   true,
	}
}

func (o Options) telemetries() *common.TelemetriesLaunchSpecification {
	if !o.EnableLogs && !o.EnableMetrics {
		return nil
	}
	t := &common.TelemetriesLaunchSpecification{}
	if o.EnableLogs {
		t.Telemetries.Logs = &common.Telemetry{
			Protocol: o.Protocol, Provider: o.Provider, Endpoint: o.LogsEndpoint, Name: "perf-logs",
		}
	}
	if o.EnableMetrics {
		t.Telemetries.Metrics = &common.Telemetry{
			Protocol: o.Protocol, Provider: o.Provider, Endpoint: o.MetricsEndpoint, Name: "perf-metrics",
		}
	}
	return t
}

// Message builds the synthetic creation-queue message for the given shape.
func Message(shape Shape, o Options) (function.CreationQueueMessage, error) {
	switch shape {
	case ShapeContainer:
		return containerMessage(o), nil
	case ShapeHelm:
		return helmMessage(o), nil
	default:
		return function.CreationQueueMessage{}, fmt.Errorf("unknown shape %q (want %q or %q)", shape, ShapeContainer, ShapeHelm)
	}
}

func baseMessage() function.CreationQueueMessage {
	return function.CreationQueueMessage{
		CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
			Action:            common.FunctionCreationAction,
			RequestID:         "perf-request",
			MessageBatchID:    "perf-batch",
			NCAID:             "perf-nca",
			InstanceCount:     1,
			InstanceTypeValue: instanceTypeValue,
			GPUType:           "PERF",
			RequestedGPUCount: 1,
		},
		Details: function.Details{
			FunctionID:        "perf-function",
			FunctionVersionID: "perf-version",
			FunctionType:      function.FunctionTypeDefault,
		},
	}
}

func containerMessage(o Options) function.CreationQueueMessage {
	msg := baseMessage()
	msg.LaunchSpecification = &function.LaunchSpecification{
		EnvironmentB64: encodeTextEnv(map[string]string{
			common.ContainerFunctionImageEnv: o.InferenceImage,
			common.UtilsImageEnv:             o.UtilsImage,
			common.InitImageEnv:              o.InitImage,
			common.BYOOOTelCollectorImageEnv: o.CollectorImage,
		}),
		ICMSEnvironment: "perf",
		CloudProvider:   "perf",
		Telemetries:     o.telemetries(),
	}
	return msg
}

func helmMessage(o Options) function.CreationQueueMessage {
	msg := baseMessage()
	msg.LaunchSpecification = &function.LaunchSpecification{
		EnvironmentB64: encodeTextEnv(map[string]string{
			common.UtilsImageEnv:             o.UtilsImage,
			common.InitImageEnv:              o.InitImage,
			common.BYOOOTelCollectorImageEnv: o.CollectorImage,
		}),
		ICMSEnvironment: "perf",
		CloudProvider:   "perf",
		Telemetries:     o.telemetries(),
		HelmChartLaunchSpecification: &common.HelmChartLaunchSpecification{
			HelmChartURL: DefaultHelmChartURL,
		},
	}
	return msg
}

// TranslateConfig returns the NVCA-equivalent translate config for the shape.
func TranslateConfig(shape Shape, o Options) function.TranslateConfig {
	tc := common.TranslateConfig{
		Namespace:                    o.Namespace,
		ObjectNameBase:               o.ObjectNameBase,
		InstanceTypeLabelSelectorKey: InstanceTypeLabelKey,
		ClusterRegion:                o.ClusterRegion,
		ClusterName:                  o.ClusterName,
	}
	if shape == ShapeContainer {
		// Container translation requires a non-zero nvidia.com/* GPU resource.
		tc.WorkloadResources = corev1.ResourceRequirements{
			Limits: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("1")},
		}
	}
	return function.TranslateConfig{TranslateConfig: tc}
}

// encodeTextEnv encodes a KEY=VALUE environment block the way NVCA supplies it
// in LaunchSpecification.EnvironmentB64: sorted lines, base64-encoded.
func encodeTextEnv(env map[string]string) string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		lines = append(lines, fmt.Sprintf("%s=%s", k, env[k]))
	}
	return base64.StdEncoding.EncodeToString([]byte(strings.Join(lines, "\n")))
}
