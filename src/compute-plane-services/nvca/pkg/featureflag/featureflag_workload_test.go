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

package featureflag

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
)

func workloadConfigCM(configYAML string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		Data: map[string]string{WorkloadConfigDataKey: configYAML},
	}
}

func TestDecodeWorkloadConfig(t *testing.T) {
	tests := []struct {
		name    string
		cm      *corev1.ConfigMap
		want    v1alpha1.WorkloadConfig
		wantErr string
	}{
		{
			name: "nil ConfigMap yields zero config",
			cm:   nil,
			want: v1alpha1.WorkloadConfig{},
		},
		{
			name: "ConfigMap without config.yaml key yields zero config",
			cm:   &corev1.ConfigMap{Data: map[string]string{"other": "x"}},
			want: v1alpha1.WorkloadConfig{},
		},
		{
			name: "empty config.yaml yields zero config",
			cm:   workloadConfigCM(""),
			want: v1alpha1.WorkloadConfig{},
		},
		{
			name: "feature flag enabled",
			cm: workloadConfigCM(`featureFlags:
  STATUS_BY_WORKER_READINESS: true
`),
			want: v1alpha1.WorkloadConfig{
				FeatureFlags: map[string]bool{StatusByWorkerReadiness: true},
			},
		},
		{
			name: "feature flag disabled",
			cm: workloadConfigCM(`featureFlags:
  STATUS_BY_WORKER_READINESS: false
`),
			want: v1alpha1.WorkloadConfig{
				FeatureFlags: map[string]bool{StatusByWorkerReadiness: false},
			},
		},
		{
			name: "unknown feature flag is dropped with warning",
			cm: workloadConfigCM(`featureFlags:
  STATUS_BY_WORKER_READINESS: true
  SOME_UNKNOWN_FLAG: true
`),
			want: v1alpha1.WorkloadConfig{
				FeatureFlags: map[string]bool{StatusByWorkerReadiness: true},
			},
		},
		{
			name:    "invalid yaml returns error",
			cm:      workloadConfigCM("featureFlags: [not-a-map"),
			wantErr: "decode workload config",
		},
		{
			name:    "wrong type for feature flag value returns error",
			cm:      workloadConfigCM("featureFlags:\n  STATUS_BY_WORKER_READINESS: notabool\n"),
			wantErr: "decode workload config",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DecodeWorkloadConfig(context.Background(), logr.Discard(), tt.cm)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestWorkloadConfigConfigMapName(t *testing.T) {
	assert.Equal(t, "nvcf-workload-config", WorkloadConfigConfigMapName)
	assert.Equal(t, "config.yaml", WorkloadConfigDataKey)
}

func TestWorkloadConfigIsFeatureFlagEnabled(t *testing.T) {
	var zero v1alpha1.WorkloadConfig
	assert.False(t, zero.IsFeatureFlagEnabled(StatusByWorkerReadiness))

	cfg := v1alpha1.WorkloadConfig{
		FeatureFlags: map[string]bool{StatusByWorkerReadiness: true},
	}
	assert.True(t, cfg.IsFeatureFlagEnabled(StatusByWorkerReadiness))
	assert.False(t, cfg.IsFeatureFlagEnabled("SOME_OTHER_FLAG"))
}
