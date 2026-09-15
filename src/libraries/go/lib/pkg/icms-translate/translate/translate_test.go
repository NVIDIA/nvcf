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

package translate

import (
	_ "embed"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/task"
)

var (
	//go:embed testdata/1.function-queue-message.json
	functionQueueMessage1 []byte
	//go:embed testdata/2.function-queue-message.json
	functionQueueMessage2 []byte
	//go:embed testdata/3.function-queue-message-container.json
	functionQueueMessage3 []byte
	//go:embed testdata/4.function-queue-message-helm.json
	functionQueueMessage4 []byte
	//go:embed testdata/5.function-queue-message-telemetries.json
	functionQueueMessage5 []byte
	//go:embed testdata/6.function-queue-message-merge.json
	functionQueueMessage6 []byte
	//go:embed testdata/7.function-queue-message-icms-action.json
	functionQueueMessage7 []byte

	//go:embed testdata/1.task-queue-message.json
	taskQueueMessage1 []byte
	//go:embed testdata/2.task-queue-message-telemetries.json
	taskQueueMessage2 []byte
	//go:embed testdata/3.task-queue-message-merge.json
	taskQueueMessage3 []byte
	//go:embed testdata/4.task-queue-message-icms-action.json
	taskQueueMessage4 []byte
)

func TestDecodeCreationQueueMessage_function_launchSpecification(t *testing.T) {
	type spec struct {
		name     string
		msgBytes []byte
		expMsg   function.CreationQueueMessage
	}

	cases := []spec{
		{
			name:     "container",
			msgBytes: functionQueueMessage3,
			expMsg: function.CreationQueueMessage{
				CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
					RequestID:         "9f93ffa2-0376-495e-a723-f6c653ed1e34",
					NCAID:             "_lILXB-1NfNmBnQSk_spqVWOtCAXQm50UEMwj3TRgymJJ2Ayuwcgxq",
					Action:            "RequestICMSInstances",
					MessageBatchID:    "14d2ec9c-25d8-4712-9288-b682570671d6",
					AccountName:       "foobar",
					InstanceCount:     1,
					InstanceType:      "DGX-CLOUD.GPU.L40",
					InstanceTypeName:  "DGX-CLOUD.GPU.L40_2x",
					InstanceTypeValue: "DGX-CLOUD.GPU.L40",
					RequestedGPUCount: 2,
					GPUType:           "L40",
				},
				Details: function.Details{
					FunctionType:      "DEFAULT",
					FunctionID:        "5a3d4a7e-9ee3-4762-8d37-d3b40a6f84c6",
					FunctionVersionID: "2c948d9b-db5d-4f93-8c29-f5d8a5d89cb9",
				},
				LaunchSpecification: &function.LaunchSpecification{
					EnvironmentB64:  "R1BVX05BTUU9TDQwCg==",
					ICMSEnvironment: "stage",
					CloudProvider:   "DGXCLOUD",
				},
			},
		},
		{
			name:     "helm",
			msgBytes: functionQueueMessage4,
			expMsg: function.CreationQueueMessage{
				CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
					RequestID:         "9f93ffa2-0376-495e-a723-f6c653ed1e34",
					NCAID:             "_lILXB-1NfNmBnQSk_spqVWOtCAXQm50UEMwj3TRgymJJ2Ayuwcgxq",
					Action:            "RequestICMSInstances",
					MessageBatchID:    "14d2ec9c-25d8-4712-9288-b682570671d6",
					AccountName:       "foobar",
					InstanceCount:     1,
					InstanceType:      "DGX-CLOUD.GPU.L40",
					InstanceTypeName:  "DGX-CLOUD.GPU.L40_2x",
					InstanceTypeValue: "DGX-CLOUD.GPU.L40",
					RequestedGPUCount: 2,
					GPUType:           "L40",
				},
				Details: function.Details{
					FunctionType:      "DEFAULT",
					FunctionID:        "5a3d4a7e-9ee3-4762-8d37-d3b40a6f84c6",
					FunctionVersionID: "2c948d9b-db5d-4f93-8c29-f5d8a5d89cb9",
				},
				LaunchSpecification: &function.LaunchSpecification{
					HelmChartLaunchSpecification: &common.HelmChartLaunchSpecification{
						HelmChartURL: "https://helm.example.com/myorg/myteam/charts/image-segmentation-1.0.3.tgz",
						Values:       []byte(`{"replicaCount":1}`),
					},
					EnvironmentB64:  "R1BVX05BTUU9TDQwCg==",
					ICMSEnvironment: "stage",
					CloudProvider:   "DGXCLOUD",
					GPUName:         "L40",
					CacheLaunchSpecification: &common.CacheLaunchSpecification{
						CacheArtifacts: false,
					},
				},
			},
		},
		{
			name:     "telemetries",
			msgBytes: functionQueueMessage5,
			expMsg: function.CreationQueueMessage{
				CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
					RequestID:         "9f93ffa2-0376-495e-a723-f6c653ed1e34",
					NCAID:             "_lILXB-1NfNmBnQSk_spqVWOtCAXQm50UEMwj3TRgymJJ2Ayuwcgxq",
					Action:            "RequestICMSInstances",
					MessageBatchID:    "14d2ec9c-25d8-4712-9288-b682570671d6",
					AccountName:       "foobar",
					InstanceCount:     1,
					InstanceType:      "DGX-CLOUD.GPU.L40",
					InstanceTypeName:  "DGX-CLOUD.GPU.L40_2x",
					InstanceTypeValue: "DGX-CLOUD.GPU.L40",
					RequestedGPUCount: 2,
					GPUType:           "L40",
				},
				Details: function.Details{
					FunctionType:      "DEFAULT",
					FunctionID:        "5a3d4a7e-9ee3-4762-8d37-d3b40a6f84c6",
					FunctionVersionID: "2c948d9b-db5d-4f93-8c29-f5d8a5d89cb9",
				},
				LaunchSpecification: &function.LaunchSpecification{
					EnvironmentB64:  "R1BVX05BTUU9TDQwCg==",
					ICMSEnvironment: "stage",
					CloudProvider:   "DGXCLOUD",
					Telemetries: &common.TelemetriesLaunchSpecification{
						Telemetries: struct {
							Logs    *common.Telemetry `json:"logsTelemetry,omitempty"`
							Metrics *common.Telemetry `json:"metricsTelemetry,omitempty"`
							Traces  *common.Telemetry `json:"tracesTelemetry,omitempty"`
						}{
							Logs: &common.Telemetry{
								Protocol: "http",
								Endpoint: "endpoint",
								Provider: "GRAFANA_CLOUD",
								Name:     "telemetry-foo",
							},
							Metrics: &common.Telemetry{
								Protocol: "http",
								Endpoint: "endpoint",
								Provider: "GRAFANA_CLOUD",
								Name:     "telemetry-baz",
							},
							Traces: &common.Telemetry{
								Protocol: "http",
								Endpoint: "endpoint",
								Provider: "GRAFANA_CLOUD",
								Name:     "telemetry-bar",
							},
						},
					},
				},
			},
		},
		{
			name:     "merge",
			msgBytes: functionQueueMessage6,
			expMsg: function.CreationQueueMessage{
				CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
					RequestID:         "9f93ffa2-0376-495e-a723-f6c653ed1e34",
					NCAID:             "_lILXB-1NfNmBnQSk_spqVWOtCAXQm50UEMwj3TRgymJJ2Ayuwcgxq",
					Action:            "RequestICMSInstances",
					MessageBatchID:    "14d2ec9c-25d8-4712-9288-b682570671d6",
					AccountName:       "foobar",
					InstanceCount:     1,
					InstanceType:      "ON-PREM.GPU.H100",
					InstanceTypeName:  "ON-PREM.GPU.H100_1x",
					InstanceTypeValue: "ON-PREM.GPU.H100",
					RequestedGPUCount: 1,
					GPUType:           "H100",
				},
				Details: function.Details{
					FunctionID:        "5a3d4a7e-9ee3-4762-8d37-d3b40a6f84c6",
					FunctionVersionID: "2c948d9b-db5d-4f93-8c29-f5d8a5d89cb9",
				},
				LaunchArtifacts: function.LaunchArtifacts{
					{
						Type:          "POD_SPEC",
						Specification: "dmFsdWUK",
					},
					{
						Type:          "SECRET_SPEC",
						Specification: "dmFsdWUK",
					},
					{
						Type:          "SECRET_SPEC",
						Specification: "dmFsdWUK",
					},
				},
				LaunchSpecification: &function.LaunchSpecification{
					GPUName: "H100",
				},
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			gotMsg, err := DecodeCreationQueueMessage(tt.msgBytes)
			if assert.NoError(t, err) {
				assert.Equal(t, tt.expMsg, gotMsg)
			}
		})
	}
}

func TestDecodeCreationQueueMessage_function_launchArtifacts(t *testing.T) {
	expMsg := function.CreationQueueMessage{
		CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
			RequestID:         "9f93ffa2-0376-495e-a723-f6c653ed1e34",
			NCAID:             "_lILXB-1NfNmBnQSk_spqVWOtCAXQm50UEMwj3TRgymJJ2Ayuwcgxq",
			Action:            "RequestICMSInstances",
			MessageBatchID:    "14d2ec9c-25d8-4712-9288-b682570671d6",
			AccountName:       "foobar",
			InstanceCount:     1,
			InstanceType:      "ON-PREM.GPU.H100",
			RequestedGPUCount: 1,
			GPUType:           "H100",
		},
		Details: function.Details{
			FunctionID:        "5a3d4a7e-9ee3-4762-8d37-d3b40a6f84c6",
			FunctionVersionID: "2c948d9b-db5d-4f93-8c29-f5d8a5d89cb9",
		},
		LaunchArtifacts: function.LaunchArtifacts{
			{
				Type:          "POD_SPEC",
				Specification: "dmFsdWUK",
			},
			{
				Type:          "SECRET_SPEC",
				Specification: "dmFsdWUK",
			},
			{
				Type:          "SECRET_SPEC",
				Specification: "dmFsdWUK",
			},
		},
		// This is set because the message has a launchSpecification field even though
		// its values are "null".
		LaunchSpecification: &function.LaunchSpecification{},
	}

	msg1, err := DecodeCreationQueueMessage(functionQueueMessage1)
	assert.NoError(t, err)
	assert.Equal(t, expMsg, msg1)

	msg2, err := DecodeCreationQueueMessage(functionQueueMessage2)
	assert.NoError(t, err)
	assert.Equal(t, expMsg, msg2)

	// Test with ICMS action name - should keep the ICMS action name (already normalized)
	msg3, err := DecodeCreationQueueMessage(functionQueueMessage7)
	assert.NoError(t, err)
	assert.Equal(t, expMsg, msg3)
}

func TestDecodeCreationQueueMessage_task(t *testing.T) {
	type spec struct {
		name     string
		msgBytes []byte
		expMsg   task.CreationQueueMessage
	}

	cases := []spec{
		{
			name:     "base",
			msgBytes: taskQueueMessage1,
			expMsg: task.CreationQueueMessage{
				CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
					RequestID:         "9f93ffa2-0376-495e-a723-f6c653ed1e34",
					NCAID:             "_lILXB-1NfNmBnQSk_spqVWOtCAXQm50UEMwj3TRgymJJ2Ayuwcgxq",
					Action:            "RequestICMSInstancesForTask",
					MessageBatchID:    "14d2ec9c-25d8-4712-9288-b682570671d6",
					AccountName:       "foobar",
					InstanceCount:     1,
					InstanceType:      "DGX-CLOUD.GPU.L40",
					InstanceTypeName:  "DGX-CLOUD.GPU.L40_2x",
					InstanceTypeValue: "DGX-CLOUD.GPU.L40",
					RequestedGPUCount: 2,
					GPUType:           "L40",
				},
				Details: task.Details{
					TaskID:   "13e2b599-96ca-42b5-a419-8fa7f701d5d2",
					TaskType: "CONTAINER",
				},
				LaunchSpecification: task.LaunchSpecification{
					ContainerImage:                 "staging.registry.example.com/myorg/gpt-3.5-turbo-fine-tune:1.0.0",
					EnvironmentB64:                 "R1BVX05BTUU9TDQwCg==",
					ResultHandlingStrategy:         "UPLOAD",
					TerminationGracePeriodDuration: "PT10M",
					MaxRuntimeDuration:             "PT2H",
					MaxQueuedDuration:              "PT2M",
					ICMSEnvironment:                "stage",
					CloudProvider:                  "DGXCLOUD",
				},
			},
		},
		{
			name:     "telemetries",
			msgBytes: taskQueueMessage2,
			expMsg: task.CreationQueueMessage{
				CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
					RequestID:         "9f93ffa2-0376-495e-a723-f6c653ed1e34",
					NCAID:             "_lILXB-1NfNmBnQSk_spqVWOtCAXQm50UEMwj3TRgymJJ2Ayuwcgxq",
					Action:            "RequestICMSInstancesForTask",
					MessageBatchID:    "14d2ec9c-25d8-4712-9288-b682570671d6",
					AccountName:       "foobar",
					InstanceCount:     1,
					InstanceType:      "DGX-CLOUD.GPU.L40",
					InstanceTypeName:  "DGX-CLOUD.GPU.L40_2x",
					InstanceTypeValue: "DGX-CLOUD.GPU.L40",
					RequestedGPUCount: 2,
					GPUType:           "L40",
				},
				Details: task.Details{
					TaskID:   "13e2b599-96ca-42b5-a419-8fa7f701d5d2",
					TaskType: "CONTAINER",
				},
				LaunchSpecification: task.LaunchSpecification{
					ContainerImage:                 "staging.registry.example.com/myorg/gpt-3.5-turbo-fine-tune:1.0.0",
					EnvironmentB64:                 "R1BVX05BTUU9TDQwCg==",
					ResultHandlingStrategy:         "UPLOAD",
					TerminationGracePeriodDuration: "PT10M",
					MaxRuntimeDuration:             "PT2H",
					MaxQueuedDuration:              "PT2M",
					ICMSEnvironment:                "stage",
					CloudProvider:                  "DGXCLOUD",
					Telemetries: &common.TelemetriesLaunchSpecification{
						Telemetries: struct {
							Logs    *common.Telemetry `json:"logsTelemetry,omitempty"`
							Metrics *common.Telemetry `json:"metricsTelemetry,omitempty"`
							Traces  *common.Telemetry `json:"tracesTelemetry,omitempty"`
						}{
							Logs: &common.Telemetry{
								Protocol: "http",
								Endpoint: "endpoint",
								Provider: "GRAFANA_CLOUD",
								Name:     "telemetry-foo",
							},
							Metrics: &common.Telemetry{
								Protocol: "http",
								Endpoint: "endpoint",
								Provider: "GRAFANA_CLOUD",
								Name:     "telemetry-baz",
							},
							Traces: &common.Telemetry{
								Protocol: "http",
								Endpoint: "endpoint",
								Provider: "GRAFANA_CLOUD",
								Name:     "telemetry-bar",
							},
						},
					},
				},
			},
		},
		{
			name:     "merge",
			msgBytes: taskQueueMessage3,
			expMsg: task.CreationQueueMessage{
				CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
					RequestID:         "9f93ffa2-0376-495e-a723-f6c653ed1e34",
					NCAID:             "_lILXB-1NfNmBnQSk_spqVWOtCAXQm50UEMwj3TRgymJJ2Ayuwcgxq",
					Action:            "RequestICMSInstancesForTask",
					MessageBatchID:    "14d2ec9c-25d8-4712-9288-b682570671d6",
					AccountName:       "foobar",
					InstanceCount:     1,
					InstanceType:      "DGX-CLOUD.GPU.L40",
					InstanceTypeName:  "DGX-CLOUD.GPU.L40_2x",
					InstanceTypeValue: "DGX-CLOUD.GPU.L40",
					RequestedGPUCount: 2,
					GPUType:           "L40",
				},
				Details: task.Details{
					TaskID:   "13e2b599-96ca-42b5-a419-8fa7f701d5d2",
					TaskType: "CONTAINER",
				},
				LaunchSpecification: task.LaunchSpecification{
					ContainerImage:                 "staging.registry.example.com/myorg/gpt-3.5-turbo-fine-tune:1.0.0",
					EnvironmentB64:                 "R1BVX05BTUU9TDQwCg==",
					ResultHandlingStrategy:         "UPLOAD",
					TerminationGracePeriodDuration: "PT10M",
					MaxRuntimeDuration:             "PT2H",
					MaxQueuedDuration:              "PT2M",
					ICMSEnvironment:                "stage",
					CloudProvider:                  "DGXCLOUD",
				},
			},
		},
		{
			name:     "icms_action_name",
			msgBytes: taskQueueMessage4,
			expMsg: task.CreationQueueMessage{
				CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
					RequestID:         "9f93ffa2-0376-495e-a723-f6c653ed1e34",
					NCAID:             "_lILXB-1NfNmBnQSk_spqVWOtCAXQm50UEMwj3TRgymJJ2Ayuwcgxq",
					Action:            "RequestICMSInstancesForTask",
					MessageBatchID:    "14d2ec9c-25d8-4712-9288-b682570671d6",
					AccountName:       "foobar",
					InstanceCount:     1,
					InstanceType:      "DGX-CLOUD.GPU.L40",
					InstanceTypeName:  "DGX-CLOUD.GPU.L40_2x",
					InstanceTypeValue: "DGX-CLOUD.GPU.L40",
					RequestedGPUCount: 2,
					GPUType:           "L40",
				},
				Details: task.Details{
					TaskID:   "13e2b599-96ca-42b5-a419-8fa7f701d5d2",
					TaskType: "CONTAINER",
				},
				LaunchSpecification: task.LaunchSpecification{
					ContainerImage:                 "staging.registry.example.com/myorg/gpt-3.5-turbo-fine-tune:1.0.0",
					EnvironmentB64:                 "R1BVX05BTUU9TDQwCg==",
					ResultHandlingStrategy:         "UPLOAD",
					TerminationGracePeriodDuration: "PT10M",
					MaxRuntimeDuration:             "PT2H",
					MaxQueuedDuration:              "PT2M",
					ICMSEnvironment:                "stage",
					CloudProvider:                  "DGXCLOUD",
				},
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			msg, err := DecodeCreationQueueMessage(tt.msgBytes)
			assert.NoError(t, err)
			assert.Equal(t, tt.expMsg, msg)
		})
	}
}

func Test_parseGPUTypeFromInstanceType(t *testing.T) {
	type testTemplate struct {
		InstanceType string
		Expected     string
		ExpectedErr  error
	}

	tests := []testTemplate{
		{
			InstanceType: "ON-PREM.GPU.H100",
			Expected:     "H100",
			ExpectedErr:  nil,
		},
		{
			InstanceType: "ON-PREM.GPU.H100.FAIL",
			Expected:     "",
			ExpectedErr:  fmt.Errorf("failed to parse the GPU name from the instanceType: %s", "ON-PREM.GPU.H100.FAIL"),
		},
	}

	for _, test := range tests {
		t.Run(fmt.Sprintf("testing with input %s", test.InstanceType), func(t *testing.T) {
			actual, err := parseGPUTypeFromInstanceType(test.InstanceType)
			assert.Equal(t, test.Expected, actual)
			assert.Equal(t, test.ExpectedErr, err)
		})
	}
}
