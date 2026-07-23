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
		want    *v1alpha1.WorkloadConfig
		wantErr bool
	}{
		{
			name: "nil ConfigMap yields nil config",
			cm:   nil,
			want: nil,
		},
		{
			name: "ConfigMap without config.yaml key yields nil config",
			cm:   &corev1.ConfigMap{Data: map[string]string{"other": "x"}},
			want: nil,
		},
		{
			name: "empty config.yaml yields nil config",
			cm:   workloadConfigCM(""),
			want: nil,
		},
		{
			name: "null config.yaml yields nil config",
			cm:   workloadConfigCM("null"),
			want: &v1alpha1.WorkloadConfig{},
		},
		{
			name: "feature flag enabled",
			cm:   workloadConfigCM("featureFlags:\n  " + StatusByWorkerReadiness + ": true\n"),
			want: &v1alpha1.WorkloadConfig{
				FeatureFlags: map[string]bool{StatusByWorkerReadiness: true},
			},
		},
		{
			name: "feature flag disabled",
			cm:   workloadConfigCM("featureFlags:\n  " + StatusByWorkerReadiness + ": false\n"),
			want: &v1alpha1.WorkloadConfig{
				FeatureFlags: map[string]bool{StatusByWorkerReadiness: false},
			},
		},
		{
			name: "unknown feature flag is dropped",
			cm:   workloadConfigCM("featureFlags:\n  " + StatusByWorkerReadiness + ": true\n  SomeUnknownFlag: true\n"),
			want: &v1alpha1.WorkloadConfig{
				FeatureFlags: map[string]bool{StatusByWorkerReadiness: true},
			},
		},
		{
			name:    "invalid yaml returns error",
			cm:      workloadConfigCM("featureFlags: [not-a-map"),
			wantErr: true,
		},
		{
			name:    "wrong type for feature flag value returns error",
			cm:      workloadConfigCM("featureFlags:\n  " + StatusByWorkerReadiness + ": notabool\n"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DecodeWorkloadConfig(context.Background(), logr.Discard(), tt.cm)
			if tt.wantErr {
				require.Error(t, err)
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
	var nilCfg *v1alpha1.WorkloadConfig
	assert.False(t, nilCfg.IsFeatureFlagEnabled(StatusByWorkerReadiness))

	empty := &v1alpha1.WorkloadConfig{}
	assert.False(t, empty.IsFeatureFlagEnabled(StatusByWorkerReadiness))

	cfg := &v1alpha1.WorkloadConfig{
		FeatureFlags: map[string]bool{StatusByWorkerReadiness: true},
	}
	assert.True(t, cfg.IsFeatureFlagEnabled(StatusByWorkerReadiness))
	assert.False(t, cfg.IsFeatureFlagEnabled("SomeOtherFlag"))
}
