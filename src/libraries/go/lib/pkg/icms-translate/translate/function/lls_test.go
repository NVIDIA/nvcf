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

package function

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
)

func TestGetLLSEnvSet(t *testing.T) {
	// Save original env values
	originalZoneDNS := os.Getenv(NVCFSBSZoneDNSEnv)
	originalStreamingInterface := os.Getenv(NVCFStreamingInterfaceEnv)

	// Restore original env values after test
	defer func() {
		os.Setenv(NVCFSBSZoneDNSEnv, originalZoneDNS)
		os.Setenv(NVCFStreamingInterfaceEnv, originalStreamingInterface)
	}()

	tests := []struct {
		name    string
		envVars map[string]string
		want    []corev1.EnvVar
	}{
		{
			name: "custom env vars",
			envVars: map[string]string{
				NVCFSBSZoneDNSEnv:         "http://custom-sbs:8000",
				NVCFStreamingInterfaceEnv: "CUSTOM",
			},
			want: []corev1.EnvVar{
				{
					Name: "NVCF_WORKER_NODE_IP",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "status.hostIP",
						},
					},
				},
				{
					Name:  "ZONE_DNS",
					Value: "http://custom-sbs:8000",
				},
				{
					Name:  "STREAMING_INTERFACE",
					Value: "CUSTOM",
				},
			},
		},
		{
			name:    "default values",
			envVars: map[string]string{},
			want: []corev1.EnvVar{
				{
					Name: "NVCF_WORKER_NODE_IP",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "status.hostIP",
						},
					},
				},
				{
					Name:  "ZONE_DNS",
					Value: defaultZoneDNS,
				},
				{
					Name:  "STREAMING_INTERFACE",
					Value: defaultStreamingInterface,
				},
			},
		},
		{
			name: "empty env vars",
			envVars: map[string]string{
				NVCFSBSZoneDNSEnv:         "",
				NVCFStreamingInterfaceEnv: "",
			},
			want: []corev1.EnvVar{
				{
					Name: "NVCF_WORKER_NODE_IP",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "status.hostIP",
						},
					},
				},
				{
					Name:  "ZONE_DNS",
					Value: defaultZoneDNS,
				},
				{
					Name:  "STREAMING_INTERFACE",
					Value: defaultStreamingInterface,
				},
			},
		},
		{
			name: "partial env vars",
			envVars: map[string]string{
				NVCFSBSZoneDNSEnv: "http://partial-sbs:8000",
			},
			want: []corev1.EnvVar{
				{
					Name: "NVCF_WORKER_NODE_IP",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "status.hostIP",
						},
					},
				},
				{
					Name:  "ZONE_DNS",
					Value: "http://partial-sbs:8000",
				},
				{
					Name:  "STREAMING_INTERFACE",
					Value: defaultStreamingInterface,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear environment variables
			os.Unsetenv(NVCFSBSZoneDNSEnv)
			os.Unsetenv(NVCFStreamingInterfaceEnv)

			// Set test environment variables if provided
			for key, value := range tt.envVars {
				os.Setenv(key, value)
			}

			got := getLLSEnvSet()
			assert.Equal(t, tt.want, got)
		})
	}
}
