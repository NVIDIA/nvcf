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
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	nvcav1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
)

func TestResolveInternalPersistentStorageConfig(t *testing.T) {
	sevenGi := resource.MustParse("7Gi")
	nineGi := resource.MustParse("9Gi")
	tests := []struct {
		name                 string
		config               nvcaconfig.InternalPersistentStorageConfig
		environmentValue     string
		expectedSpec         InternalPersistentStorageSpec
		expectedSource       string
		expectedOverride     bool
		expectedErrorMessage string
	}{
		{
			name:           "not configured",
			expectedSpec:   InternalPersistentStorageSpec{},
			expectedSource: "default",
		},
		{
			name: "agent config enables internal persistent storage",
			config: nvcaconfig.InternalPersistentStorageConfig{
				StorageClassName: "gp2",
				HardResourceQuota: nvcaconfig.ResourceList{
					corev1.ResourceRequestsStorage: sevenGi,
				},
			},
			expectedSpec: InternalPersistentStorageSpec{
				Enabled:          true,
				StorageClassName: "gp2",
				ResourceQuota: nvcav1new.InternalPersistentStorageResourceQuotaSpec{
					Hard: corev1.ResourceList{corev1.ResourceRequestsStorage: sevenGi},
				},
			},
			expectedSource: "agent-config",
		},
		{
			name:   "agent config allows the default quota",
			config: nvcaconfig.InternalPersistentStorageConfig{StorageClassName: "gp2"},
			expectedSpec: InternalPersistentStorageSpec{
				Enabled:          true,
				StorageClassName: "gp2",
				ResourceQuota:    nvcav1new.InternalPersistentStorageResourceQuotaSpec{},
			},
			expectedSource: "agent-config",
		},
		{
			name: "quota without storage class is rejected",
			config: nvcaconfig.InternalPersistentStorageConfig{
				HardResourceQuota: nvcaconfig.ResourceList{
					corev1.ResourceRequestsStorage: sevenGi,
				},
			},
			expectedErrorMessage: "storageClassName is required",
		},
		{
			name:             "environment configuration overrides agent config",
			config:           nvcaconfig.InternalPersistentStorageConfig{StorageClassName: "gp2"},
			environmentValue: base64.StdEncoding.EncodeToString([]byte(`{"enabled":true,"storageClassName":"premium","resourceQuota":{"hard":{"requests.storage":"9Gi"}}}`)),
			expectedSpec: InternalPersistentStorageSpec{
				Enabled:          true,
				StorageClassName: "premium",
				ResourceQuota: nvcav1new.InternalPersistentStorageResourceQuotaSpec{
					Hard: corev1.ResourceList{corev1.ResourceRequestsStorage: nineGi},
				},
			},
			expectedSource:   "environment-override",
			expectedOverride: true,
		},
		{
			name:             "environment configuration can explicitly disable agent config",
			config:           nvcaconfig.InternalPersistentStorageConfig{StorageClassName: "gp2"},
			environmentValue: base64.StdEncoding.EncodeToString([]byte(`{"enabled":false}`)),
			expectedSpec:     InternalPersistentStorageSpec{},
			expectedSource:   "environment-override",
			expectedOverride: true,
		},
		{
			name:                 "invalid base64 environment configuration is rejected",
			environmentValue:     "not-base64",
			expectedErrorMessage: "decode base64 environment variable",
		},
		{
			name:                 "invalid JSON environment configuration is rejected",
			environmentValue:     base64.StdEncoding.EncodeToString([]byte("not-json")),
			expectedErrorMessage: "decode JSON environment variable",
		},
		{
			name:                 "environment configuration requires enabled",
			environmentValue:     base64.StdEncoding.EncodeToString([]byte(`{"storageClassName":"premium"}`)),
			expectedErrorMessage: "requires a non-null enabled field",
		},
		{
			name:                 "environment configuration rejects null enabled",
			environmentValue:     base64.StdEncoding.EncodeToString([]byte(`{"enabled":null}`)),
			expectedErrorMessage: "requires a non-null enabled field",
		},
		{
			name:                 "environment configuration rejects unknown fields",
			environmentValue:     base64.StdEncoding.EncodeToString([]byte(`{"enabled":false,"unexpected":true}`)),
			expectedErrorMessage: "unknown field",
		},
		{
			name:                 "environment configuration rejects trailing JSON",
			environmentValue:     base64.StdEncoding.EncodeToString([]byte(`{"enabled":false}{"enabled":true}`)),
			expectedErrorMessage: "expected exactly one JSON object",
		},
		{
			name:                 "enabled environment configuration requires a storage class",
			environmentValue:     base64.StdEncoding.EncodeToString([]byte(`{"enabled":true}`)),
			expectedErrorMessage: "requires storageClassName when enabled",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(nvcaInternalPersistentStorageConfigJSONBase64Key, tt.environmentValue)

			actualSpec, actualSource, actualOverride, err := resolveInternalPersistentStorageConfig(tt.config)
			if tt.expectedErrorMessage != "" {
				require.ErrorContains(t, err, tt.expectedErrorMessage)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expectedSpec, actualSpec)
			assert.Equal(t, tt.expectedSource, actualSource)
			assert.Equal(t, tt.expectedOverride, actualOverride)
		})
	}
}

func TestConfigureHelmInternalPersistentStorage(t *testing.T) {
	t.Setenv(nvcaInternalPersistentStorageConfigJSONBase64Key, "")
	previousSpec := HelmInternalPersistentStorage.Spec
	previousEnabled := HelmInternalPersistentStorage.enabled
	t.Cleanup(func() {
		HelmInternalPersistentStorage.Spec = previousSpec
		HelmInternalPersistentStorage.enabled = previousEnabled
	})

	logger := logrus.New()
	logger.SetLevel(logrus.PanicLevel)
	ctx := core.WithLogger(context.Background(), logrus.NewEntry(logger))
	require.NoError(t, ConfigureHelmInternalPersistentStorage(ctx, nvcaconfig.InternalPersistentStorageConfig{
		StorageClassName: "gp2",
		HardResourceQuota: nvcaconfig.ResourceList{
			corev1.ResourceRequestsStorage: resource.MustParse("7Gi"),
		},
	}))

	assert.True(t, HelmInternalPersistentStorage.Enabled())
	assert.Equal(t, "gp2", HelmInternalPersistentStorage.Spec.StorageClassName)
	assert.Equal(t, resource.MustParse("7Gi"),
		HelmInternalPersistentStorage.Spec.ResourceQuota.Hard[corev1.ResourceRequestsStorage])
}

func TestInternalPersistentStorageSpec_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name      string
		jsonBytes []byte
		expected  InternalPersistentStorageSpec
		expectErr bool
	}{
		{
			name:      "valid JSON",
			jsonBytes: []byte(`{"enabled": true, "storageClassName": "test-storage-class"}`),
			expected:  InternalPersistentStorageSpec{Enabled: true, StorageClassName: "test-storage-class"},
			expectErr: false,
		},
		{
			name:      "invalid JSON",
			jsonBytes: []byte(" invalid JSON"),
			expected:  InternalPersistentStorageSpec{},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var spec InternalPersistentStorageSpec
			err := json.Unmarshal(tt.jsonBytes, &spec)
			if tt.expectErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, spec)
			}
		})
	}
}
