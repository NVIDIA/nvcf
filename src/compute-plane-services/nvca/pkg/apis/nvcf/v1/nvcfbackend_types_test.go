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

package v1

import (
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/stretchr/testify/assert"
)

func TestMiniServiceConfig(t *testing.T) {
	msCfg := &MiniServiceConfig{}

	msCfgTmp := msCfg.Complete(EnvTypeStage)
	assert.Equal(t, &MiniServiceConfig{
		HelmReValServiceURL: helmReValServiceURLStg,
		CacheDirSize:        defaultReValCacheDirSize,
	}, msCfgTmp)
	msCfgTmp = msCfg.Complete(EnvTypeProd)
	assert.Equal(t, &MiniServiceConfig{
		HelmReValServiceURL: helmReValServiceURLProd,
		CacheDirSize:        defaultReValCacheDirSize,
	}, msCfgTmp)
}

func TestFNDServiceConfig_IsEnabled(t *testing.T) {
	tests := []struct {
		name         string
		config       *FNDServiceConfig
		featureFlags []string
		expected     bool
	}{
		{
			name:     "nil config, no feature flags",
			config:   nil,
			expected: false,
		},
		{
			name:     "nil enabled field, no feature flags",
			config:   &FNDServiceConfig{},
			expected: false,
		},
		{
			name: "enabled false, no feature flags",
			config: &FNDServiceConfig{
				Enabled: boolPtr(false),
			},
			expected: false,
		},
		{
			name: "enabled true, no feature flags",
			config: &FNDServiceConfig{
				Enabled: boolPtr(true),
			},
			expected: true,
		},
		{
			name:         "nil config, with FNDS feature flag",
			config:       nil,
			featureFlags: []string{featureflag.UseFunctionDeploymentStages.Key},
			expected:     true,
		},
		{
			name:         "nil enabled field, with FNDS feature flag",
			config:       &FNDServiceConfig{},
			featureFlags: []string{featureflag.UseFunctionDeploymentStages.Key},
			expected:     true,
		},
		{
			name: "enabled false, with FNDS feature flag",
			config: &FNDServiceConfig{
				Enabled: boolPtr(false),
			},
			featureFlags: []string{featureflag.UseFunctionDeploymentStages.Key},
			expected:     true,
		},
		{
			name: "enabled true, with FNDS feature flag",
			config: &FNDServiceConfig{
				Enabled: boolPtr(true),
			},
			featureFlags: []string{featureflag.UseFunctionDeploymentStages.Key},
			expected:     true,
		},
		{
			name:         "nil config, with other feature flags",
			config:       nil,
			featureFlags: []string{featureflag.GXCache.Key},
			expected:     false,
		},
		{
			name: "enabled false, with multiple feature flags including FNDS",
			config: &FNDServiceConfig{
				Enabled: boolPtr(false),
			},
			featureFlags: []string{featureflag.UseFunctionDeploymentStages.Key, featureflag.GXCache.Key},
			expected:     true,
		},
		{
			name: "enabled true, with multiple feature flags not including FNDS",
			config: &FNDServiceConfig{
				Enabled: boolPtr(true),
			},
			featureFlags: []string{featureflag.GXCache.Key},
			expected:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.config.IsEnabled(tt.featureFlags))
		})
	}
}

func TestFNDServiceConfig_Complete(t *testing.T) {
	tests := []struct {
		name         string
		config       *FNDServiceConfig
		envType      EnvType
		featureFlags []string
		expected     *FNDServiceConfig
	}{
		{
			name:     "nil config, no feature flags",
			config:   nil,
			envType:  EnvTypeProd,
			expected: nil,
		},
		{
			name: "disabled config, no feature flags",
			config: &FNDServiceConfig{
				Enabled: boolPtr(false),
			},
			envType: EnvTypeProd,
			expected: &FNDServiceConfig{
				Enabled: boolPtr(false),
			},
		},
		{
			name: "enabled config with empty URL - prod env",
			config: &FNDServiceConfig{
				Enabled: boolPtr(true),
			},
			envType: EnvTypeProd,
			expected: &FNDServiceConfig{
				Enabled:    boolPtr(true),
				ServiceURL: FunctionDeploymentStagesServiceURLProd,
			},
		},
		{
			name: "enabled config with empty URL - stage env",
			config: &FNDServiceConfig{
				Enabled: boolPtr(true),
			},
			envType: EnvTypeStage,
			expected: &FNDServiceConfig{
				Enabled:    boolPtr(true),
				ServiceURL: FunctionDeploymentStagesServiceURLStg,
			},
		},
		{
			name: "enabled config with custom URL",
			config: &FNDServiceConfig{
				Enabled:    boolPtr(true),
				ServiceURL: "https://custom.url",
			},
			envType: EnvTypeProd,
			expected: &FNDServiceConfig{
				Enabled:    boolPtr(true),
				ServiceURL: "https://custom.url",
			},
		},
		{
			name:         "nil config with FNDS feature flag - prod env",
			config:       nil,
			envType:      EnvTypeProd,
			featureFlags: []string{featureflag.UseFunctionDeploymentStages.Key},
			expected: &FNDServiceConfig{
				Enabled:    boolPtr(true),
				ServiceURL: FunctionDeploymentStagesServiceURLProd,
			},
		},
		{
			name:         "nil config with FNDS feature flag - stage env",
			config:       nil,
			envType:      EnvTypeStage,
			featureFlags: []string{featureflag.UseFunctionDeploymentStages.Key},
			expected: &FNDServiceConfig{
				Enabled:    boolPtr(true),
				ServiceURL: FunctionDeploymentStagesServiceURLStg,
			},
		},
		{
			name:         "nil enabled field with FNDS feature flag - prod env",
			config:       &FNDServiceConfig{},
			envType:      EnvTypeProd,
			featureFlags: []string{featureflag.UseFunctionDeploymentStages.Key},
			expected: &FNDServiceConfig{
				Enabled:    boolPtr(true),
				ServiceURL: FunctionDeploymentStagesServiceURLProd,
			},
		},
		{
			name: "disabled config with FNDS feature flag - stage env",
			config: &FNDServiceConfig{
				Enabled: boolPtr(false),
			},
			envType:      EnvTypeStage,
			featureFlags: []string{featureflag.UseFunctionDeploymentStages.Key},
			expected: &FNDServiceConfig{
				Enabled:    boolPtr(true),
				ServiceURL: FunctionDeploymentStagesServiceURLStg,
			},
		},
		{
			name: "disabled config with custom URL and FNDS feature flag",
			config: &FNDServiceConfig{
				Enabled:    boolPtr(false),
				ServiceURL: "https://custom.url",
			},
			envType:      EnvTypeProd,
			featureFlags: []string{featureflag.UseFunctionDeploymentStages.Key},
			expected: &FNDServiceConfig{
				Enabled:    boolPtr(true),
				ServiceURL: "https://custom.url",
			},
		},
		{
			name: "disabled config with other feature flags",
			config: &FNDServiceConfig{
				Enabled: boolPtr(false),
			},
			envType:      EnvTypeProd,
			featureFlags: []string{featureflag.GXCache.Key},
			expected: &FNDServiceConfig{
				Enabled: boolPtr(false),
			},
		},
		{
			name: "enabled config with multiple feature flags including FNDS",
			config: &FNDServiceConfig{
				Enabled: boolPtr(true),
			},
			envType:      EnvTypeStage,
			featureFlags: []string{featureflag.UseFunctionDeploymentStages.Key, featureflag.GXCache.Key},
			expected: &FNDServiceConfig{
				Enabled:    boolPtr(true),
				ServiceURL: FunctionDeploymentStagesServiceURLStg,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.config.Complete(tt.envType, tt.featureFlags)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func boolPtr(b bool) *bool {
	return &b
}

func TestImageConfig_BuildImageRef(t *testing.T) {
	tests := []struct {
		name     string
		config   ImageConfig
		expected string
	}{
		{
			name:     "empty repository",
			config:   ImageConfig{Repository: "", Tag: "v1.0.0"},
			expected: "",
		},
		{
			name:     "empty tag",
			config:   ImageConfig{Repository: "nvcr.io/nvidia/test", Tag: ""},
			expected: "",
		},
		{
			name:     "normal tag",
			config:   ImageConfig{Repository: "nvcr.io/nvidia/test", Tag: "v1.0.0"},
			expected: "nvcr.io/nvidia/test:v1.0.0",
		},
		{
			name:     "sha256 digest",
			config:   ImageConfig{Repository: "nvcr.io/nvidia/test", Tag: "sha256:abc123"},
			expected: "nvcr.io/nvidia/test@sha256:abc123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.config.BuildImageRef())
		})
	}
}

func TestImageCredHelperConfig_Complete(t *testing.T) {
	tests := []struct {
		name    string
		config  *ImageCredHelperConfig
		envType EnvType
	}{
		{
			name:    "nil config",
			config:  nil,
			envType: EnvTypeProd,
		},
		{
			name:    "empty config",
			config:  &ImageCredHelperConfig{},
			envType: EnvTypeStage,
		},
		{
			name: "config with image",
			config: &ImageCredHelperConfig{
				ImageConfig: ImageConfig{Repository: "test", Tag: "v1"},
			},
			envType: EnvTypeProd,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.config.Complete(tt.envType)
			assert.NotNil(t, result)
			if tt.config != nil {
				assert.Equal(t, tt.config.ImageConfig, result.ImageConfig)
			}
		})
	}
}

func TestOTelCollectorConfig_IsEnabled(t *testing.T) {
	tests := []struct {
		name     string
		config   *OTelCollectorConfig
		expected bool
	}{
		{
			name:     "nil config",
			config:   nil,
			expected: false,
		},
		{
			name:     "empty config (disabled)",
			config:   &OTelCollectorConfig{},
			expected: false,
		},
		{
			name:     "enabled true",
			config:   &OTelCollectorConfig{Enabled: true},
			expected: true,
		},
		{
			name:     "enabled false",
			config:   &OTelCollectorConfig{Enabled: false},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.config.IsEnabled())
		})
	}
}

func TestOTelCollectorConfig_Complete(t *testing.T) {
	tests := []struct {
		name    string
		config  *OTelCollectorConfig
		envType EnvType
	}{
		{
			name:    "nil config",
			config:  nil,
			envType: EnvTypeProd,
		},
		{
			name:    "empty config",
			config:  &OTelCollectorConfig{},
			envType: EnvTypeStage,
		},
		{
			name:    "enabled config",
			config:  &OTelCollectorConfig{Enabled: true},
			envType: EnvTypeProd,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.config.Complete(tt.envType)
			assert.NotNil(t, result)
			if tt.config != nil {
				assert.Equal(t, tt.config.Enabled, result.Enabled)
			}
		})
	}
}
