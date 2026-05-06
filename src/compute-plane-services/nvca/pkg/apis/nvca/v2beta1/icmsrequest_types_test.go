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

package v2beta1

import (
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/task"
	"github.com/stretchr/testify/assert"
)

func TestICMSRequestStatus_GetInstanceIDs(t *testing.T) {
	type test struct {
		name     string
		input    ICMSRequestStatus
		expected []string
	}

	tests := []test{
		{
			name:     "no-instances",
			input:    ICMSRequestStatus{},
			expected: nil,
		},
		{
			name: "with a single instance",
			input: ICMSRequestStatus{
				Instances: map[string]InstanceStatus{
					"abcd-1234": {ID: "abcd-1234"},
				},
			},
			expected: []string{"abcd-1234"},
		},
		{
			name: "with multiple instance",
			input: ICMSRequestStatus{
				Instances: map[string]InstanceStatus{
					"abcd-1234": {ID: "abcd-1234"},
					"abcd-5":    {ID: "abcd-5"},
				},
			},
			expected: []string{"abcd-1234", "abcd-5"},
		},
	}

	for _, testGetInstance := range tests {
		t.Run(testGetInstance.name, func(t *testing.T) {
			assert.ElementsMatch(t, testGetInstance.expected, testGetInstance.input.GetInstanceIDs())
		})
	}
}

func TestICMSRequestSpec_GetICMSEnvironment(t *testing.T) {
	tests := []struct {
		name string
		spec ICMSRequestSpec
		want string
	}{
		{
			name: "function launch specification with ICMS environment",
			spec: ICMSRequestSpec{
				CreationMsgInfo: ICMSCreationMessageInfo{
					FunctionLaunchSpecification: &function.LaunchSpecification{
						ICMSEnvironment: "prod",
					},
				},
			},
			want: "prod",
		},
		{
			name: "task launch specification with ICMS environment",
			spec: ICMSRequestSpec{
				CreationMsgInfo: ICMSCreationMessageInfo{
					TaskLaunchSpecification: &task.LaunchSpecification{
						ICMSEnvironment: "dev",
					},
				},
			},
			want: "dev",
		},
		{
			name: "no launch specifications",
			spec: ICMSRequestSpec{
				CreationMsgInfo: ICMSCreationMessageInfo{},
			},
			want: "",
		},
		{
			name: "both launch specifications present, function takes precedence",
			spec: ICMSRequestSpec{
				CreationMsgInfo: ICMSCreationMessageInfo{
					FunctionLaunchSpecification: &function.LaunchSpecification{
						ICMSEnvironment: "prod",
					},
					TaskLaunchSpecification: &task.LaunchSpecification{
						ICMSEnvironment: "dev",
					},
				},
			},
			want: "prod",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.spec.GetICMSEnvironment(); got != tt.want {
				t.Errorf("ICMSRequestSpec.GetICMSEnvironment() = %v, want %v", got, tt.want)
			}
		})
	}
}
