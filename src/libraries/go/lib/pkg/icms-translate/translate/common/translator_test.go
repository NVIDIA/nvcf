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
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestAddNVIDIAGPUNoScheduleToleration(t *testing.T) {
	tests := []struct {
		name     string
		podSpec  *corev1.PodSpec
		expected bool
	}{
		{
			name: "empty tolerations",
			podSpec: &corev1.PodSpec{
				Tolerations: []corev1.Toleration{},
			},
			expected: true,
		},
		{
			name: "existing nvidia gpu toleration",
			podSpec: &corev1.PodSpec{
				Tolerations: []corev1.Toleration{
					{
						Key:      NVIDIAGPUTolerationKey,
						Operator: corev1.TolerationOpExists,
						Effect:   corev1.TaintEffectNoSchedule,
					},
				},
			},
			expected: false,
		},
		{
			name: "different existing tolerations",
			podSpec: &corev1.PodSpec{
				Tolerations: []corev1.Toleration{
					{
						Key:      "other-key",
						Operator: corev1.TolerationOpExists,
						Effect:   corev1.TaintEffectNoSchedule,
					},
				},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			added := AddNVIDIAGPUNoScheduleToleration(tt.podSpec)
			assert.Equal(t, tt.expected, added)

			// Verify toleration was added when expected
			if tt.expected {
				found := false
				for _, tol := range tt.podSpec.Tolerations {
					if tol.Key == NVIDIAGPUTolerationKey &&
						tol.Operator == corev1.TolerationOpExists &&
						tol.Effect == corev1.TaintEffectNoSchedule {
						found = true
						break
					}
				}
				assert.True(t, found, "Expected NVIDIA GPU toleration not found")
			}
		})
	}
}

func TestMergeTolerations(t *testing.T) {
	t.Run("adds_new_tolerations_and_skips_exact_duplicates", func(t *testing.T) {
		noExecuteSeconds := int64(30)
		podSpec := &corev1.PodSpec{
			Tolerations: []corev1.Toleration{{
				Key:      "existing",
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoSchedule,
			}},
		}

		added := MergeTolerations(
			podSpec,
			corev1.Toleration{
				Key:      "existing",
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoSchedule,
			},
			corev1.Toleration{
				Key:               "new",
				Operator:          corev1.TolerationOpEqual,
				Value:             "gpu",
				Effect:            corev1.TaintEffectNoExecute,
				TolerationSeconds: &noExecuteSeconds,
			},
		)

		assert.True(t, added)
		assert.Len(t, podSpec.Tolerations, 2)
		assert.Equal(t, "new", podSpec.Tolerations[1].Key)
		assert.Equal(t, int64(30), *podSpec.Tolerations[1].TolerationSeconds)
	})

	t.Run("returns_false_when_all_tolerations_already_exist", func(t *testing.T) {
		podSpec := &corev1.PodSpec{
			Tolerations: []corev1.Toleration{{
				Key:      "existing",
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoSchedule,
			}},
		}

		added := MergeTolerations(podSpec, corev1.Toleration{
			Key:      "existing",
			Operator: corev1.TolerationOpExists,
			Effect:   corev1.TaintEffectNoSchedule,
		})

		assert.False(t, added)
		assert.Len(t, podSpec.Tolerations, 1)
	})
}

func TestSetPodAffinity(t *testing.T) {
	tests := []struct {
		name string
		pod  *corev1.Pod
		pa   *corev1.PodAffinity
		paa  *corev1.PodAntiAffinity
		want *corev1.Affinity
	}{
		{
			name: "empty affinity",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Affinity: &corev1.Affinity{},
				},
			},
			pa: &corev1.PodAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
					{
						LabelSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"key": "value"},
						},
					},
				},
			},
			paa: &corev1.PodAntiAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
					{
						LabelSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"anti": "value"},
						},
					},
				},
			},
			want: &corev1.Affinity{
				PodAffinity: &corev1.PodAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
						{
							LabelSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{"key": "value"},
							},
						},
					},
				},
				PodAntiAffinity: &corev1.PodAntiAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
						{
							LabelSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{"anti": "value"},
							},
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			SetPodAffinity(tt.pod, tt.pa, tt.paa)
			assert.Equal(t, tt.want, tt.pod.Spec.Affinity)
		})
	}
}

func TestNewCommonMetadata(t *testing.T) {
	testCases := []struct {
		name       string
		metadata   CreationQueueMessageMetadata
		tcfg       TranslateConfig
		icmsEnv    string
		opts       MetadataOptions
		wantLabels map[string]string
		wantAnnos  map[string]string
	}{
		{
			name: "basic metadata with minimal inputs",
			metadata: CreationQueueMessageMetadata{
				RequestID:         "req-123",
				NCAID:             "nca-123",
				MessageBatchID:    "batch-123",
				GPUType:           "a100",
				InstanceTypeValue: "g4dn",
				RequestedGPUCount: 2,
			},
			tcfg: TranslateConfig{
				ClusterRegion: "us-west-2",
				ClusterName:   "test-cluster",
			},
			icmsEnv: "test",
			opts:    MetadataOptions{},
			wantLabels: map[string]string{
				"icms-request-id":                 "req-123",
				"nca-id":                          "nca-nca-123-nca",
				"nvcf.nvidia.io/message-batch-id": "batch-123",
				"gpu-name":                        "a100",
				"environment":                     "test",
				"performance_class":               "g4dn",
				"ENVIRONMENT":                     "test",
				"GPU_COUNT":                       "2",
				NCAIDEncodedEnvKey:                "nca-nca-123-nca",
			},
			wantAnnos: map[string]string{
				"nca-id":                             "nca-123",
				"instance-count":                     "0",
				"nvcf.nvidia.io/region":              "us-west-2",
				"nvcf.nvidia.io/backend":             "test-cluster",
				"nvcf.nvidia.io/instance-type-name":  "",
				"nvcf.nvidia.io/instance-type-value": "g4dn",
				"nvcf.nvidia.io/environment":         "test",
			},
		},
		{
			name: "metadata with function details",
			metadata: CreationQueueMessageMetadata{
				RequestID:          "req-456",
				NCAID:              "nca-456",
				MessageBatchID:     "batch-456",
				GPUType:            "a100",
				InstanceTypeValue:  "g4dn",
				InstanceTypeName:   "g4dn_2x",
				RequestedGPUCount:  2,
				DeploymentID:       "dpl-456",
				GPUSpecificationID: "gpuspec-456",
			},
			tcfg: TranslateConfig{
				ClusterRegion: "us-west-2",
				ClusterName:   "test-cluster",
				CommonLabels: map[string]string{
					"common-label": "value",
				},
				CommonAnnotations: map[string]string{
					"common-anno": "value",
				},
			},
			icmsEnv: "prod",
			opts: MetadataOptions{
				FunctionID:        "func-123",
				FunctionVersionID: "func-ver-123",
			},
			wantLabels: map[string]string{
				"icms-request-id":                     "req-456",
				"nca-id":                              "nca-nca-456-nca",
				"nvcf.nvidia.io/message-batch-id":     "batch-456",
				"gpu-name":                            "a100",
				"environment":                         "prod",
				"performance_class":                   "g4dn",
				"ENVIRONMENT":                         "prod",
				"GPU_COUNT":                           "2",
				NCAIDEncodedEnvKey:                    "nca-nca-456-nca",
				"function-id":                         "func-123",
				"FUNCTION_ID":                         "func-123",
				"function-version-id":                 "func-ver-123",
				"FUNCTION_VERSION_ID":                 "func-ver-123",
				"nvcf.nvidia.io/deployment-id":        "dpl-456",
				"nvcf.nvidia.io/gpu-specification-id": "gpuspec-456",
				"common-label":                        "value",
			},
			wantAnnos: map[string]string{
				"nca-id":                             "nca-456",
				"instance-count":                     "0",
				"nvcf.nvidia.io/region":              "us-west-2",
				"nvcf.nvidia.io/backend":             "test-cluster",
				"nvcf.nvidia.io/instance-type-name":  "g4dn_2x",
				"nvcf.nvidia.io/instance-type-value": "g4dn",
				"nvcf.nvidia.io/environment":         "prod",
				"common-anno":                        "value",
			},
		},
		{
			name: "metadata with task details",
			metadata: CreationQueueMessageMetadata{
				RequestID:         "req-789",
				NCAID:             "nca-789",
				MessageBatchID:    "batch-789",
				GPUType:           "a100",
				InstanceTypeValue: "g4dn",
				InstanceTypeName:  "g4dn_2x",
				InstanceCount:     1,
				RequestedGPUCount: 2,
			},
			tcfg: TranslateConfig{
				ClusterRegion: "us-west-2",
				ClusterName:   "test-cluster",
			},
			icmsEnv: "dev",
			opts: MetadataOptions{
				TaskID: "task-123",
			},
			wantLabels: map[string]string{
				"icms-request-id":                 "req-789",
				"nca-id":                          "nca-nca-789-nca",
				"nvcf.nvidia.io/message-batch-id": "batch-789",
				"gpu-name":                        "a100",
				"environment":                     "dev",
				"performance_class":               "g4dn",
				"ENVIRONMENT":                     "dev",
				"GPU_COUNT":                       "2",
				NCAIDEncodedEnvKey:                "nca-nca-789-nca",
				"task-id":                         "task-123",
				"TASK_ID":                         "task-123",
			},
			wantAnnos: map[string]string{
				"nca-id":                             "nca-789",
				"instance-count":                     "1",
				"nvcf.nvidia.io/region":              "us-west-2",
				"nvcf.nvidia.io/backend":             "test-cluster",
				"nvcf.nvidia.io/instance-type-name":  "g4dn_2x",
				"nvcf.nvidia.io/instance-type-value": "g4dn",
				"nvcf.nvidia.io/environment":         "dev",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			labels, annos := NewCommonMetadata(tc.metadata, tc.tcfg, tc.icmsEnv, tc.opts)

			// Test labels
			for k, v := range tc.wantLabels {
				if got, ok := labels[k]; !ok || got != v {
					t.Errorf("Labels[%q] = %q, want %q", k, got, v)
				}
			}

			// Test annotations
			for k, v := range tc.wantAnnos {
				if got, ok := annos[k]; !ok || got != v {
					t.Errorf("Annotations[%q] = %q, want %q", k, got, v)
				}
			}
		})
	}
}
