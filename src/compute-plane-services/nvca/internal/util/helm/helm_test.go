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

package helm

import (
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/task"
	"github.com/stretchr/testify/assert"

	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
)

func TestIsMiniServiceType(t *testing.T) {
	tests := []struct {
		name     string
		request  *nvcav2beta1.ICMSRequest
		expected bool
	}{
		{
			name: "function request with helm chart launch specification",
			request: &nvcav2beta1.ICMSRequest{
				Spec: nvcav2beta1.ICMSRequestSpec{
					CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
						FunctionLaunchSpecification: &function.LaunchSpecification{
							HelmChartLaunchSpecification: &common.HelmChartLaunchSpecification{},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "task request with helm chart launch specification",
			request: &nvcav2beta1.ICMSRequest{
				Spec: nvcav2beta1.ICMSRequestSpec{
					CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
						TaskLaunchSpecification: &task.LaunchSpecification{
							HelmChartLaunchSpecification: &common.HelmChartLaunchSpecification{},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "request with helm chart launch artifact",
			request: &nvcav2beta1.ICMSRequest{
				Spec: nvcav2beta1.ICMSRequestSpec{
					CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
						LaunchArtifacts: []function.LaunchArtifact{
							{
								Type: function.LaunchArtifactTypeHelmChart,
							},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "request with multiple launch artifacts including helm chart",
			request: &nvcav2beta1.ICMSRequest{
				Spec: nvcav2beta1.ICMSRequestSpec{
					CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
						LaunchArtifacts: []function.LaunchArtifact{
							{
								Type: "other-type",
							},
							{
								Type: function.LaunchArtifactTypeHelmChart,
							},
							{
								Type: "another-type",
							},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "request with both helm chart launch artifact and launch specification",
			request: &nvcav2beta1.ICMSRequest{
				Spec: nvcav2beta1.ICMSRequestSpec{
					CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
						LaunchArtifacts: []function.LaunchArtifact{
							{
								Type: function.LaunchArtifactTypeHelmChart,
							},
						},
						FunctionLaunchSpecification: &function.LaunchSpecification{
							HelmChartLaunchSpecification: &common.HelmChartLaunchSpecification{},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "function request with helm chart launch specification and other fields",
			request: &nvcav2beta1.ICMSRequest{
				Spec: nvcav2beta1.ICMSRequestSpec{
					CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
						FunctionLaunchSpecification: &function.LaunchSpecification{
							HelmChartLaunchSpecification: &common.HelmChartLaunchSpecification{},
							CacheLaunchSpecification:     &common.CacheLaunchSpecification{},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "task request with helm chart launch specification and other fields",
			request: &nvcav2beta1.ICMSRequest{
				Spec: nvcav2beta1.ICMSRequestSpec{
					CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
						TaskLaunchSpecification: &task.LaunchSpecification{
							HelmChartLaunchSpecification: &common.HelmChartLaunchSpecification{},
							CacheLaunchSpecification:     &common.CacheLaunchSpecification{},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "function request without helm chart launch specification",
			request: &nvcav2beta1.ICMSRequest{
				Spec: nvcav2beta1.ICMSRequestSpec{
					CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
						FunctionLaunchSpecification: &function.LaunchSpecification{
							CacheLaunchSpecification: &common.CacheLaunchSpecification{},
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "task request without helm chart launch specification",
			request: &nvcav2beta1.ICMSRequest{
				Spec: nvcav2beta1.ICMSRequestSpec{
					CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
						TaskLaunchSpecification: &task.LaunchSpecification{
							CacheLaunchSpecification: &common.CacheLaunchSpecification{},
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "request with non-helm chart launch artifacts",
			request: &nvcav2beta1.ICMSRequest{
				Spec: nvcav2beta1.ICMSRequestSpec{
					CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
						LaunchArtifacts: []function.LaunchArtifact{
							{
								Type: "other-type",
							},
							{
								Type: "another-type",
							},
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "request with empty launch artifacts",
			request: &nvcav2beta1.ICMSRequest{
				Spec: nvcav2beta1.ICMSRequestSpec{
					CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
						LaunchArtifacts: []function.LaunchArtifact{},
					},
				},
			},
			expected: false,
		},
		{
			name: "request with nil function launch specification",
			request: &nvcav2beta1.ICMSRequest{
				Spec: nvcav2beta1.ICMSRequestSpec{
					CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
						FunctionLaunchSpecification: nil,
					},
				},
			},
			expected: false,
		},
		{
			name: "request with nil task launch specification",
			request: &nvcav2beta1.ICMSRequest{
				Spec: nvcav2beta1.ICMSRequestSpec{
					CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
						TaskLaunchSpecification: nil,
					},
				},
			},
			expected: false,
		},
		{
			name: "request with both function and task launch specifications, only function has helm chart",
			request: &nvcav2beta1.ICMSRequest{
				Spec: nvcav2beta1.ICMSRequestSpec{
					CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
						FunctionLaunchSpecification: &function.LaunchSpecification{
							HelmChartLaunchSpecification: &common.HelmChartLaunchSpecification{},
						},
						TaskLaunchSpecification: &task.LaunchSpecification{
							CacheLaunchSpecification: &common.CacheLaunchSpecification{},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "request with both function and task launch specifications, only task has helm chart",
			request: &nvcav2beta1.ICMSRequest{
				Spec: nvcav2beta1.ICMSRequestSpec{
					CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
						FunctionLaunchSpecification: &function.LaunchSpecification{
							CacheLaunchSpecification: &common.CacheLaunchSpecification{},
						},
						TaskLaunchSpecification: &task.LaunchSpecification{
							HelmChartLaunchSpecification: &common.HelmChartLaunchSpecification{},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "request with both function and task launch specifications, both have helm chart",
			request: &nvcav2beta1.ICMSRequest{
				Spec: nvcav2beta1.ICMSRequestSpec{
					CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
						FunctionLaunchSpecification: &function.LaunchSpecification{
							HelmChartLaunchSpecification: &common.HelmChartLaunchSpecification{},
						},
						TaskLaunchSpecification: &task.LaunchSpecification{
							HelmChartLaunchSpecification: &common.HelmChartLaunchSpecification{},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "request with both function and task launch specifications, neither has helm chart",
			request: &nvcav2beta1.ICMSRequest{
				Spec: nvcav2beta1.ICMSRequestSpec{
					CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
						FunctionLaunchSpecification: &function.LaunchSpecification{
							CacheLaunchSpecification: &common.CacheLaunchSpecification{},
						},
						TaskLaunchSpecification: &task.LaunchSpecification{
							CacheLaunchSpecification: &common.CacheLaunchSpecification{},
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "empty request",
			request: &nvcav2beta1.ICMSRequest{
				Spec: nvcav2beta1.ICMSRequestSpec{
					CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isMiniServiceType(tt.request)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIsMiniServiceCreateRequest(t *testing.T) {
	tests := []struct {
		name     string
		request  *nvcav2beta1.ICMSRequest
		expected bool
	}{
		{
			name: "function creation with helm chart",
			request: &nvcav2beta1.ICMSRequest{
				Spec: nvcav2beta1.ICMSRequestSpec{
					Action: common.FunctionCreationAction,
					CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
						FunctionLaunchSpecification: &function.LaunchSpecification{
							HelmChartLaunchSpecification: &common.HelmChartLaunchSpecification{},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "task creation with helm chart",
			request: &nvcav2beta1.ICMSRequest{
				Spec: nvcav2beta1.ICMSRequestSpec{
					Action: common.TaskCreationAction,
					CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
						TaskLaunchSpecification: &task.LaunchSpecification{
							HelmChartLaunchSpecification: &common.HelmChartLaunchSpecification{},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "function creation without helm chart",
			request: &nvcav2beta1.ICMSRequest{
				Spec: nvcav2beta1.ICMSRequestSpec{
					Action: common.FunctionCreationAction,
					CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
						FunctionLaunchSpecification: &function.LaunchSpecification{
							CacheLaunchSpecification: &common.CacheLaunchSpecification{},
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "wrong action with helm chart",
			request: &nvcav2beta1.ICMSRequest{
				Spec: nvcav2beta1.ICMSRequestSpec{
					Action: "wrong_action",
					CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
						FunctionLaunchSpecification: &function.LaunchSpecification{
							HelmChartLaunchSpecification: &common.HelmChartLaunchSpecification{},
						},
					},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsMiniServiceCreateRequest(tt.request)
			assert.Equal(t, tt.expected, result)
		})
	}
}
