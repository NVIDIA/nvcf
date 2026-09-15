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
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"

	nvcav1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

func TestNewHelmInternalPersistentStorageFeatureFlag(t *testing.T) {
	tests := []struct {
		name            string
		envVarValue     string
		defaultParam    bool
		expectedEnabled bool
		expectedSpec    InternalPersistentStorageSpec
	}{
		{
			name:            "no env var",
			envVarValue:     "",
			defaultParam:    false,
			expectedEnabled: false,
			expectedSpec: InternalPersistentStorageSpec{
				Enabled: false,
			},
		},
		{
			name:            "invalid base64 env var",
			envVarValue:     " invalid base64",
			defaultParam:    false,
			expectedEnabled: false,
			expectedSpec: InternalPersistentStorageSpec{
				Enabled: false,
			},
		},
		{
			name:            "invalid JSON env var",
			envVarValue:     base64.StdEncoding.EncodeToString([]byte(" invalid JSON")),
			defaultParam:    false,
			expectedEnabled: false,
			expectedSpec: InternalPersistentStorageSpec{
				Enabled: false,
			},
		},
		{
			name:            "valid env var with empty storage class name",
			envVarValue:     base64.StdEncoding.EncodeToString([]byte(`{"enabled": false, "storageClassName": ""}`)),
			defaultParam:    false,
			expectedEnabled: false,
			expectedSpec: InternalPersistentStorageSpec{
				Enabled: false,
			},
		},
		{
			name:            "valid env var with storage class name",
			envVarValue:     base64.StdEncoding.EncodeToString([]byte(`{"enabled": true, "storageClassName": "test-storage-class"}`)),
			defaultParam:    false,
			expectedEnabled: true,
			expectedSpec: InternalPersistentStorageSpec{
				Enabled:          true,
				StorageClassName: "test-storage-class",
			},
		},
		{
			name:            "valid env var with storage class name but disabled",
			envVarValue:     base64.StdEncoding.EncodeToString([]byte(`{"enabled": false, "storageClassName": "test-storage-class"}`)),
			defaultParam:    true,
			expectedEnabled: false,
			expectedSpec: InternalPersistentStorageSpec{
				Enabled:          false,
				StorageClassName: "test-storage-class",
			},
		},
		{
			name:            "valid env var with resource quota",
			envVarValue:     base64.StdEncoding.EncodeToString([]byte(`{"enabled": true, "storageClassName": "test-storage-class", "resourceQuota": {"hard": {"cpu": "1", "memory": "1Gi"}}}`)),
			defaultParam:    false,
			expectedEnabled: true,
			expectedSpec: InternalPersistentStorageSpec{
				Enabled:          true,
				StorageClassName: "test-storage-class",
				ResourceQuota: nvcav1new.InternalPersistentStorageResourceQuotaSpec{
					Hard: corev1.ResourceList{
						"cpu":    resource.MustParse("1"),
						"memory": resource.MustParse("1Gi"),
					},
				},
			},
		},
		{
			name:            "valid env var with resource quota but disabled",
			envVarValue:     base64.StdEncoding.EncodeToString([]byte(`{"enabled": false, "storageClassName": "test-storage-class", "resourceQuota": {"hard": {"cpu": "1", "memory": "1Gi"}}}`)),
			defaultParam:    true,
			expectedEnabled: false,
			expectedSpec: InternalPersistentStorageSpec{
				Enabled:          false,
				StorageClassName: "test-storage-class",
				ResourceQuota: nvcav1new.InternalPersistentStorageResourceQuotaSpec{
					Hard: corev1.ResourceList{
						"cpu":    resource.MustParse("1"),
						"memory": resource.MustParse("1Gi"),
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv(nvcaInternalPersistentStorageConfigJSONBase64Key, tt.envVarValue)
			defer os.Unsetenv(nvcaInternalPersistentStorageConfigJSONBase64Key)

			f := newHelmInternalPersistentStorageFeatureFlag(tt.defaultParam)
			assert.Equal(t, tt.expectedEnabled, *f.enabled)
			assert.Equal(t, tt.expectedSpec, f.Spec)

			assert.Equal(t, tt.expectedEnabled, f.Enabled())
		})
	}
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
