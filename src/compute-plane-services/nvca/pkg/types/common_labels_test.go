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

package types

import (
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/task"
	"github.com/stretchr/testify/assert"

	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
)

func TestGetNCAIDLabelVal(t *testing.T) {
	tests := []struct {
		name     string
		labels   map[string]string
		expected string
		found    bool
	}{
		{
			name:     "empty labels",
			labels:   map[string]string{},
			expected: "",
			found:    false,
		},
		{
			name: "no NCA ID label",
			labels: map[string]string{
				"other-label": "value",
			},
			expected: "",
			found:    false,
		},
		{
			name: "NCA ID label with prefix and suffix",
			labels: map[string]string{
				NCAIDKey: "nca-123-nca",
			},
			expected: "123",
			found:    true,
		},
		{
			name: "NCA ID label without prefix and suffix",
			labels: map[string]string{
				NCAIDKey: "123",
			},
			expected: "123",
			found:    true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual, found := GetNCAIDLabelVal(test.labels)
			assert.Equal(t, test.expected, actual)
			assert.Equal(t, test.found, found)
		})
	}
}

func TestMakeNCAIDLabelValue(t *testing.T) {
	tests := []struct {
		name     string
		ncaID    string
		expected string
	}{
		{
			name:     "empty NCA ID",
			ncaID:    "",
			expected: "nca--nca",
		},
		{
			name:     "non-empty NCA ID",
			ncaID:    "123",
			expected: "nca-123-nca",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual := MakeNCAIDLabelValue(test.ncaID)
			assert.Equal(t, test.expected, actual)
		})
	}
}

func TestGetLabelsForRequest(t *testing.T) {
	funcReq := &nvcav2beta1.ICMSRequest{
		Spec: nvcav2beta1.ICMSRequestSpec{
			Action:            common.FunctionCreationAction,
			RequestID:         "req-123",
			FunctionID:        "func-123",
			FunctionVersionID: "func-ver-123",
			NCAId:             "nca-123",
			CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
				GPUName:      "gpu-type",
				ClusterGroup: "cluster-group",
			},
		},
	}
	taskReq := &nvcav2beta1.ICMSRequest{
		Spec: nvcav2beta1.ICMSRequestSpec{
			Action:    common.TaskCreationAction,
			RequestID: "req-123",
			TaskDetails: task.Details{
				TaskID:   "task-123",
				TaskType: "CONTAINER",
			},
			NCAId: "nca-123",
			CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
				GPUName:      "gpu-type",
				ClusterGroup: "cluster-group",
				TaskLaunchSpecification: &task.LaunchSpecification{
					Telemetries: &common.TelemetriesLaunchSpecification{},
				},
			},
		},
	}

	tests := []struct {
		name           string
		req            *nvcav2beta1.ICMSRequest
		gxCacheEnabled bool
		expected       map[string]string
	}{
		{
			name:           "func basic labels",
			req:            funcReq,
			gxCacheEnabled: true,
			expected: map[string]string{
				ICMSRequestIDKey:          "req-123",
				FunctionIDKey:             "func-123",
				FunctionIDUpperKey:        "func-123",
				FunctionVersionIDKey:      "func-ver-123",
				FunctionVersionIDUpperKey: "func-ver-123",
				NCAIDKey:                  "nca-nca-123-nca",
				NCAIDUpperKey:             "nca-nca-123-nca",
				GPUNameKey:                "gpu-type",
				MessageBatchIDKey:         "",
				ShaderCacheLabelKey:       "true",
			},
		},
		{
			name:           "task basic labels",
			req:            taskReq,
			gxCacheEnabled: true,
			expected: map[string]string{
				ICMSRequestIDKey:    "req-123",
				TaskIDKey:           "task-123",
				TaskIDUpperKey:      "task-123",
				NCAIDKey:            "nca-nca-123-nca",
				NCAIDUpperKey:       "nca-nca-123-nca",
				GPUNameKey:          "gpu-type",
				MessageBatchIDKey:   "",
				ShaderCacheLabelKey: "true",
			},
		},
		{
			name:           "func labels with feature flags enabled",
			req:            funcReq,
			gxCacheEnabled: true,
			expected: map[string]string{
				ICMSRequestIDKey:          "req-123",
				FunctionIDKey:             "func-123",
				FunctionIDUpperKey:        "func-123",
				FunctionVersionIDKey:      "func-ver-123",
				FunctionVersionIDUpperKey: "func-ver-123",
				NCAIDKey:                  "nca-nca-123-nca",
				NCAIDUpperKey:             "nca-nca-123-nca",
				GPUNameKey:                "gpu-type",
				MessageBatchIDKey:         "",
				ShaderCacheLabelKey:       "true",
			},
		},
		{
			name:           "func labels with feature flags disabled",
			req:            funcReq,
			gxCacheEnabled: false,
			expected: map[string]string{
				ICMSRequestIDKey:          "req-123",
				FunctionIDKey:             "func-123",
				FunctionIDUpperKey:        "func-123",
				FunctionVersionIDKey:      "func-ver-123",
				FunctionVersionIDUpperKey: "func-ver-123",
				NCAIDKey:                  "nca-nca-123-nca",
				NCAIDUpperKey:             "nca-nca-123-nca",
				GPUNameKey:                "gpu-type",
				MessageBatchIDKey:         "",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fff := &mockFFFetcher{
				attrEnabledFunc: func(*featureflag.Attribute) bool {
					return false
				},
				ffEnabledFunc: func(f *featureflag.FeatureFlag) bool {
					if f.Key == featureflag.GXCache.Key {
						return test.gxCacheEnabled
					}
					return false
				},
			}

			actual := GetLabelsForRequest(test.req, fff)
			assert.Equal(t, test.expected, actual)
		})
	}
}

func TestGetAnnotationsForRequest(t *testing.T) {
	tests := []struct {
		name     string
		req      *nvcav2beta1.ICMSRequest
		expected map[string]string
	}{
		{
			name: "basic annotations",
			req: &nvcav2beta1.ICMSRequest{
				Spec: nvcav2beta1.ICMSRequestSpec{
					RequestID: "req-123",
					NCAId:     "nca-123",
					CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
						CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
							InstanceCount: 3,
						},
						ClusterGroup: "cluster-group",
					},
				},
			},
			expected: map[string]string{
				ICMSRequestIDKey: "req-123",
				NCAIDKey:         "nca-123",
				ClusterGroupKey:  "cluster-group",
				InstanceCountKey: "3",
			},
		},
		{
			name: "empty creation msg info",
			req: &nvcav2beta1.ICMSRequest{
				Spec: nvcav2beta1.ICMSRequestSpec{
					RequestID:       "req-123",
					NCAId:           "nca-123",
					CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{},
				},
			},
			expected: map[string]string{
				ICMSRequestIDKey: "req-123",
				NCAIDKey:         "nca-123",
				ClusterGroupKey:  "",
				InstanceCountKey: "0",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual := GetAnnotationsForRequest(test.req)
			assert.Equal(t, test.expected, actual)
		})
	}
}

func TestGetFunctionVersionIDLabelVal(t *testing.T) {
	tests := []struct {
		name     string
		labels   map[string]string
		expected string
		ok       bool
	}{
		{
			name:     "labels is nil",
			labels:   nil,
			expected: "",
			ok:       false,
		},
		{
			name:     "labels is empty",
			labels:   map[string]string{},
			expected: "",
			ok:       false,
		},
		{
			name: "labels does not contain FunctionVersionIDKey",
			labels: map[string]string{
				"other-key": "other-value",
			},
			expected: "",
			ok:       false,
		},
		{
			name: "labels contains FunctionVersionIDKey with empty value",
			labels: map[string]string{
				FunctionVersionIDKey: "",
			},
			expected: "",
			ok:       false,
		},
		{
			name: "labels contains FunctionVersionIDKey with non-empty value",
			labels: map[string]string{
				FunctionVersionIDKey: "some-value",
			},
			expected: "some-value",
			ok:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual, ok := GetFunctionVersionIDLabelVal(tt.labels)
			assert.Equal(t, tt.expected, actual)
			assert.Equal(t, tt.ok, ok)
		})
	}
}

type mockFFFetcher struct {
	attrEnabledFunc func(*featureflag.Attribute) bool
	ffEnabledFunc   func(*featureflag.FeatureFlag) bool
}

func (f *mockFFFetcher) IsFeatureFlagEnabled(ff *featureflag.FeatureFlag) bool {
	return f.ffEnabledFunc(ff)
}

func (f *mockFFFetcher) IsAttributeEnabled(ff *featureflag.Attribute) bool {
	return f.attrEnabledFunc(ff)
}
