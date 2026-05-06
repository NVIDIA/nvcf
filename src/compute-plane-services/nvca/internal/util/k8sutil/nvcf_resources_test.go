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

package k8sutil

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"sort"
	"sync"
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
)

func TestSetConfigDefaultResources(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		t.Cleanup(func() {
			parseUtilsLimitsOnce = sync.Once{}
			resourcesUtils = corev1.ResourceList{}
			parseAddlOverheadResourcesOnce = sync.Once{}
			addlOverheadResources = corev1.ResourceList{}
		})

		cfg := nvcaconfig.Config{}
		SetConfigDefaultResources(&cfg)
		assert.Equal(t, nvcaconfig.Config{
			Agent: nvcaconfig.AgentConfig{
				UtilsResources: nvcaconfig.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("4"),
					corev1.ResourceMemory: resource.MustParse("4Gi"),
				},
				SharedStorage: nvcaconfig.SharedStorageConfig{
					Server: nvcaconfig.SharedStorageServerConfig{
						ContainerResources: nvcaconfig.ResourceRequirements{
							Requests: nvcaconfig.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("20m"),
								corev1.ResourceMemory: resource.MustParse("150Mi"),
							},
							Limits: nvcaconfig.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("500m"),
								corev1.ResourceMemory: resource.MustParse("2Gi"),
							},
						},
					},
					TaskData: nvcaconfig.SharedStorageTaskDataConfig{
						StorageCapacity: resource.MustParse("100Gi"),
					},
				},
				BYOOResources: nvcaconfig.ResourceRequirements{
					Requests: nvcaconfig.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
					Limits: nvcaconfig.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
				},
				BYOOFluentBitResources: nvcaconfig.ResourceRequirements{
					Requests: nvcaconfig.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("100m"),
						corev1.ResourceMemory: resource.MustParse("128Mi"),
					},
					Limits: nvcaconfig.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("200m"),
						corev1.ResourceMemory: resource.MustParse("256Mi"),
					},
				},
				AdditionalResourceOverhead: nvcaconfig.ResourceList{},
			},
		}, cfg)
	})

	t.Run("config overrides", func(t *testing.T) {
		t.Cleanup(func() {
			parseUtilsLimitsOnce = sync.Once{}
			resourcesUtils = corev1.ResourceList{}
			parseAddlOverheadResourcesOnce = sync.Once{}
			addlOverheadResources = corev1.ResourceList{}
		})

		cfg := nvcaconfig.Config{
			Agent: nvcaconfig.AgentConfig{
				UtilsResources: nvcaconfig.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("2"),
					corev1.ResourceMemory: resource.MustParse("2Gi"),
				},
				SharedStorage: nvcaconfig.SharedStorageConfig{
					Server: nvcaconfig.SharedStorageServerConfig{
						ContainerResources: nvcaconfig.ResourceRequirements{
							Requests: nvcaconfig.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("100m"),
								corev1.ResourceMemory: resource.MustParse("250Mi"),
							},
							Limits: nvcaconfig.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("100m"),
								corev1.ResourceMemory: resource.MustParse("250Mi"),
							},
						},
					},
					TaskData: nvcaconfig.SharedStorageTaskDataConfig{
						StorageCapacity: resource.MustParse("200Gi"),
					},
				},
				AdditionalResourceOverhead: nvcaconfig.ResourceList{},
			},
		}
		SetConfigDefaultResources(&cfg)
		assert.Equal(t, nvcaconfig.Config{
			Agent: nvcaconfig.AgentConfig{
				UtilsResources: nvcaconfig.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("2"),
					corev1.ResourceMemory: resource.MustParse("2Gi"),
				},
				SharedStorage: nvcaconfig.SharedStorageConfig{
					Server: nvcaconfig.SharedStorageServerConfig{
						ContainerResources: nvcaconfig.ResourceRequirements{
							Requests: nvcaconfig.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("100m"),
								corev1.ResourceMemory: resource.MustParse("250Mi"),
							},
							Limits: nvcaconfig.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("100m"),
								corev1.ResourceMemory: resource.MustParse("250Mi"),
							},
						},
					},
					TaskData: nvcaconfig.SharedStorageTaskDataConfig{
						StorageCapacity: resource.MustParse("200Gi"),
					},
				},
				BYOOResources: nvcaconfig.ResourceRequirements{
					Requests: nvcaconfig.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
					Limits: nvcaconfig.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
				},
				BYOOFluentBitResources: nvcaconfig.ResourceRequirements{
					Requests: nvcaconfig.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("100m"),
						corev1.ResourceMemory: resource.MustParse("128Mi"),
					},
					Limits: nvcaconfig.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("200m"),
						corev1.ResourceMemory: resource.MustParse("256Mi"),
					},
				},
				AdditionalResourceOverhead: nvcaconfig.ResourceList{},
			},
		}, cfg)
	})

	t.Run("envs and config overrides", func(t *testing.T) {
		t.Cleanup(func() {
			parseUtilsLimitsOnce = sync.Once{}
			resourcesUtils = corev1.ResourceList{}
			parseAddlOverheadResourcesOnce = sync.Once{}
			addlOverheadResources = corev1.ResourceList{}
		})

		t.Setenv("NVCA_SHARED_STORAGE_CONFIG_JSON_BASE64", base64.StdEncoding.EncodeToString([]byte(`
{
	"server": {
		"smbServerContainerResources": {
			"limits": {
				"cpu": "300m"
			}
		}
	},
	"taskData": {
		"storageClassName": "foo",
		"size": "222Gi"
	}
}
`)))
		t.Setenv("NVCF_UTILS_CPU_LIMIT", "321m")
		t.Setenv("NVCF_UTILS_EPHEMERAL_STORAGE_LIMIT", "3Gi")
		t.Setenv("NVCF_ADDITIONAL_OVERHEAD_RESOURCES_B64", base64.StdEncoding.EncodeToString([]byte(`
{
	"ephemeral-storage": "5Gi"
}
`)))

		cfg := nvcaconfig.Config{
			Agent: nvcaconfig.AgentConfig{
				UtilsResources: nvcaconfig.ResourceList{
					corev1.ResourceCPU: resource.MustParse("2"),
				},
				SharedStorage: nvcaconfig.SharedStorageConfig{
					Server: nvcaconfig.SharedStorageServerConfig{
						ContainerResources: nvcaconfig.ResourceRequirements{
							Requests: nvcaconfig.ResourceList{
								corev1.ResourceCPU: resource.MustParse("100m"),
							},
							Limits: nvcaconfig.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("100m"),
								corev1.ResourceMemory: resource.MustParse("250Mi"),
							},
						},
					},
					TaskData: nvcaconfig.SharedStorageTaskDataConfig{
						PVMountOptions:  []string{"ro"},
						StorageCapacity: resource.MustParse("200Gi"),
					},
				},
				AdditionalResourceOverhead: nvcaconfig.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("6"),
					corev1.ResourceMemory: resource.MustParse("6Gi"),
				},
			},
		}
		SetConfigDefaultResources(&cfg)
		assert.Equal(t, nvcaconfig.Config{
			Agent: nvcaconfig.AgentConfig{
				UtilsResources: nvcaconfig.ResourceList{
					corev1.ResourceCPU:              resource.MustParse("321m"),
					corev1.ResourceMemory:           resource.MustParse("4Gi"),
					corev1.ResourceEphemeralStorage: resource.MustParse("3Gi"),
				},
				SharedStorage: nvcaconfig.SharedStorageConfig{
					Server: nvcaconfig.SharedStorageServerConfig{
						ContainerResources: nvcaconfig.ResourceRequirements{
							Requests: nvcaconfig.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("100m"),
								corev1.ResourceMemory: resource.MustParse("150Mi"),
							},
							Limits: nvcaconfig.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("300m"),
								corev1.ResourceMemory: resource.MustParse("250Mi"),
							},
						},
					},
					TaskData: nvcaconfig.SharedStorageTaskDataConfig{
						StorageClassName: ptr.To("foo"),
						PVMountOptions:   []string{"ro"},
						StorageCapacity:  resource.MustParse("222Gi"),
					},
				},
				AdditionalResourceOverhead: nvcaconfig.ResourceList{
					corev1.ResourceCPU:              resource.MustParse("6"),
					corev1.ResourceMemory:           resource.MustParse("6Gi"),
					corev1.ResourceEphemeralStorage: resource.MustParse("5Gi"),
				},
				BYOOResources: nvcaconfig.ResourceRequirements{
					Requests: nvcaconfig.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
					Limits: nvcaconfig.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
				},
				BYOOFluentBitResources: nvcaconfig.ResourceRequirements{
					Requests: nvcaconfig.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("100m"),
						corev1.ResourceMemory: resource.MustParse("128Mi"),
					},
					Limits: nvcaconfig.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("200m"),
						corev1.ResourceMemory: resource.MustParse("256Mi"),
					},
				},
			},
		}, cfg)
	})
}

func TestGetAdditionalOverheadResources(t *testing.T) {
	// Save original env var to restore later
	originalEnv := os.Getenv(additionalOverheadResourcesEnv)
	defer func() {
		if originalEnv == "" {
			os.Unsetenv(additionalOverheadResourcesEnv)
		} else {
			os.Setenv(additionalOverheadResourcesEnv, originalEnv)
		}
	}()

	tests := []struct {
		name         string
		envValue     string
		expected     corev1.ResourceList
		shouldBeZero bool
		expError     bool
	}{
		{
			name:         "empty env var",
			envValue:     "",
			expected:     corev1.ResourceList{},
			shouldBeZero: true,
		},
		{
			name: "valid base64 encoded resource list",
			envValue: createBase64ResourceList(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			}),
			expected: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
		},
		{
			name:     "invalid base64",
			envValue: "invalid-base64!@#",
			expError: true,
		},
		{
			name:     "valid base64 but invalid JSON",
			envValue: base64.StdEncoding.EncodeToString([]byte("invalid-json")),
			expError: true,
		},
		{
			name: "complex resource list",
			envValue: createBase64ResourceList(corev1.ResourceList{
				corev1.ResourceCPU:              resource.MustParse("500m"),
				corev1.ResourceMemory:           resource.MustParse("1Gi"),
				corev1.ResourceEphemeralStorage: resource.MustParse("2Gi"),
				"custom.io/resource":            resource.MustParse("10"),
			}),
			expected: corev1.ResourceList{
				corev1.ResourceCPU:              resource.MustParse("500m"),
				corev1.ResourceMemory:           resource.MustParse("1Gi"),
				corev1.ResourceEphemeralStorage: resource.MustParse("2Gi"),
				"custom.io/resource":            resource.MustParse("10"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset the sync.Once to ensure we test the initialization
			parseAddlOverheadResourcesOnce = sync.Once{}
			addlOverheadResources = corev1.ResourceList{}

			// Set the env var
			if tt.envValue == "" {
				os.Unsetenv(additionalOverheadResourcesEnv)
			} else {
				os.Setenv(additionalOverheadResourcesEnv, tt.envValue)
			}

			// Call the function
			var gotCode int
			exit := func(i int) {
				gotCode = i
			}
			if tt.expError {
				getAdditionalOverheadResourcesFromEnvs(exit)
				assert.Equal(t, 1, gotCode)
				return
			}
			result := getAdditionalOverheadResourcesFromEnvs(exit)

			// Verify the result
			if tt.shouldBeZero && len(result) != 0 {
				t.Errorf("expected empty resource list, got %v", result)
			} else if !tt.shouldBeZero {
				if !resourceListsEqual(tt.expected, result) {
					t.Errorf("expected %v, got %v", tt.expected, result)
				}
			}

			// Test that subsequent calls return the same result (testing sync.Once)
			result2 := getAdditionalOverheadResourcesFromEnvs(exit)
			if !resourceListsEqual(result, result2) {
				t.Error("subsequent calls should return the same result")
			}

			// Verify that we get a deep copy (modifying result shouldn't affect subsequent calls)
			if len(result) > 0 {
				// Try to modify the result
				for k := range result {
					result[k] = resource.MustParse("999")
					break
				}
				result3 := getAdditionalOverheadResourcesFromEnvs(exit)
				if resourceListsEqual(result, result3) {
					t.Error("function should return a deep copy, modifications should not affect subsequent calls")
				}
			}
		})
	}
}

func TestInitAdditionalOverheadResources(t *testing.T) {
	tests := []struct {
		name         string
		envValue     string
		expected     corev1.ResourceList
		shouldBeZero bool
		expError     string
	}{
		{
			name:         "no env var",
			envValue:     "",
			expected:     corev1.ResourceList{},
			shouldBeZero: true,
		},
		{
			name: "valid resource list",
			envValue: createBase64ResourceList(corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("200m"),
			}),
			expected: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("200m"),
			},
		},
		{
			name:     "invalid base64",
			envValue: "not-valid-base64",
			expError: "illegal base64 data at input byte 3",
		},
		{
			name:     "valid base64 invalid json",
			envValue: base64.StdEncoding.EncodeToString([]byte("not json")),
			expError: "invalid character 'o' in literal null (expecting 'u')",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(key string) string {
				if key == additionalOverheadResourcesEnv {
					return tt.envValue
				}
				return ""
			}

			result, err := initAdditionalOverheadResourcesFromEnvs(getenv)
			if tt.expError != "" {
				assert.EqualError(t, err, tt.expError)
				return
			}

			assert.NoError(t, err)
			if tt.shouldBeZero && len(result) != 0 {
				t.Errorf("expected empty resource list, got %v", result)
			} else if !tt.shouldBeZero {
				if !resourceListsEqual(tt.expected, result) {
					t.Errorf("expected %v, got %v", tt.expected, result)
				}
			}
		})
	}
}

func TestGetUtilsContainerResourceDefaults(t *testing.T) {
	// Save original env vars
	originalCPU := os.Getenv(utilsCPULimitEnv)
	originalMemory := os.Getenv(utilsMemoryLimitEnv)
	originalEphemeral := os.Getenv(utilsEphemeralStorageLimitEnv)
	t.Cleanup(func() {
		restoreEnvVar(utilsCPULimitEnv, originalCPU)
		restoreEnvVar(utilsMemoryLimitEnv, originalMemory)
		restoreEnvVar(utilsEphemeralStorageLimitEnv, originalEphemeral)
	})

	tests := []struct {
		name                string
		cpuEnv              string
		memoryEnv           string
		ephemeralStorageEnv string
		expectedCPU         string
		expectedMemory      string
		expectedEphemeral   string
		expError            bool
	}{
		{
			name:                "unset",
			cpuEnv:              "",
			memoryEnv:           "",
			ephemeralStorageEnv: "",
			expectedCPU:         "",
			expectedMemory:      "",
			expectedEphemeral:   "",
		},
		{
			name:                "custom valid values",
			cpuEnv:              "2",
			memoryEnv:           "8Gi",
			ephemeralStorageEnv: "10Gi",
			expectedCPU:         "2",
			expectedMemory:      "8Gi",
			expectedEphemeral:   "10Gi",
		},
		{
			name:                "invalid CPU",
			cpuEnv:              "invalid-cpu",
			memoryEnv:           "1Gi",
			ephemeralStorageEnv: "",
			expectedCPU:         "",
			expectedMemory:      "",
			expectedEphemeral:   "",
			expError:            true,
		},
		{
			name:                "invalid memory",
			cpuEnv:              "1",
			memoryEnv:           "invalid-memory",
			ephemeralStorageEnv: "",
			expectedCPU:         "",
			expectedMemory:      "",
			expectedEphemeral:   "",
			expError:            true,
		},
		{
			name:                "zero ephemeral storage should not be included",
			cpuEnv:              "1",
			memoryEnv:           "1Gi",
			ephemeralStorageEnv: "0",
			expectedCPU:         "1",
			expectedMemory:      "1Gi",
			expectedEphemeral:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset the sync.Once to ensure we test the initialization
			parseUtilsLimitsOnce = sync.Once{}
			resourcesUtils = corev1.ResourceList{}

			// Set env vars
			setEnvVar(utilsCPULimitEnv, tt.cpuEnv)
			setEnvVar(utilsMemoryLimitEnv, tt.memoryEnv)
			setEnvVar(utilsEphemeralStorageLimitEnv, tt.ephemeralStorageEnv)

			// Call the function
			var gotCode int
			exit := func(i int) {
				gotCode = i
			}
			if tt.expError {
				getUtilsContainerResourcesFromEnvs(exit)
				assert.Equal(t, 1, gotCode)
				return
			}
			result := getUtilsContainerResourcesFromEnvs(exit)

			// Verify CPU
			if cpu, exists := result[corev1.ResourceCPU]; exists {
				if cpu.String() != tt.expectedCPU {
					t.Errorf("expected CPU %s, got %s", tt.expectedCPU, cpu.String())
				}
			} else if tt.cpuEnv != "" {
				t.Error("expected CPU to be present")
			}

			// Verify Memory
			if memory, exists := result[corev1.ResourceMemory]; exists {
				if memory.String() != tt.expectedMemory {
					t.Errorf("expected Memory %s, got %s", tt.expectedMemory, memory.String())
				}
			} else if tt.memoryEnv != "" {
				t.Error("expected Memory to be present")
			}

			// Verify Ephemeral Storage
			if es, exists := result[corev1.ResourceEphemeralStorage]; exists {
				if es.String() != tt.expectedEphemeral {
					t.Errorf("expected EphemeralStorage %s, got %s", tt.expectedEphemeral, es.String())
				}
			} else if tt.ephemeralStorageEnv != "" && tt.ephemeralStorageEnv != "0" {
				t.Error("expected EphemeralStorage to be present")
			}

			// Test deep copy behavior
			result[corev1.ResourceCPU] = resource.MustParse("999")
			result2 := getUtilsContainerResourcesFromEnvs(exit)
			cpu2 := result2[corev1.ResourceCPU]
			if cpu2.String() == "999" {
				t.Error("function should return a deep copy")
			}
		})
	}
}

func TestInitUtilsContainerResources(t *testing.T) {
	tests := []struct {
		name              string
		envValues         map[string]string
		expectedCPU       string
		expectedMemory    string
		expectedEphemeral string
		expError          string
	}{
		{
			name:              "unset",
			envValues:         map[string]string{},
			expectedCPU:       "",
			expectedMemory:    "",
			expectedEphemeral: "",
		},
		{
			name: "custom values",
			envValues: map[string]string{
				utilsCPULimitEnv:              "2",
				utilsMemoryLimitEnv:           "8Gi",
				utilsEphemeralStorageLimitEnv: "1Gi",
			},
			expectedCPU:       "2",
			expectedMemory:    "8Gi",
			expectedEphemeral: "1Gi",
		},
		{
			name: "invalid values",
			envValues: map[string]string{
				utilsCPULimitEnv:              "invalid",
				utilsMemoryLimitEnv:           "invalid",
				utilsEphemeralStorageLimitEnv: "invalid",
			},
			expError: "quantities must match the regular expression '^([+-]?[0-9.]+)([eEinumkKMGTP]*[-+]?[0-9]*)$'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(key string) string {
				if val, exists := tt.envValues[key]; exists {
					return val
				}
				return ""
			}

			result, err := initUtilsContainerResourcesFromEnvs(getenv)
			if tt.expError != "" {
				assert.EqualError(t, err, tt.expError)
				return
			}

			// Verify CPU
			if tt.expectedCPU == "" {
				assert.NotContains(t, result, corev1.ResourceCPU)
			} else {
				q := result[corev1.ResourceCPU]
				assert.Equal(t, tt.expectedCPU, q.String())
			}

			// Verify Memory
			if tt.expectedMemory == "" {
				assert.NotContains(t, result, corev1.ResourceMemory)
			} else {
				q := result[corev1.ResourceMemory]
				assert.Equal(t, tt.expectedMemory, q.String())
			}

			// Verify Ephemeral Storage
			if tt.expectedEphemeral == "" {
				assert.NotContains(t, result, corev1.ResourceEphemeralStorage)
			} else {
				q := result[corev1.ResourceEphemeralStorage]
				assert.Equal(t, tt.expectedEphemeral, q.String())
			}
		})
	}
}

func TestCombineResourceLists(t *testing.T) {
	tests := []struct {
		name     string
		input    []corev1.ResourceList
		expected corev1.ResourceList
	}{
		{
			name:     "empty input",
			input:    []corev1.ResourceList{},
			expected: corev1.ResourceList{},
		},
		{
			name: "single resource list",
			input: []corev1.ResourceList{
				{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("1Gi"),
				},
			},
			expected: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
			},
		},
		{
			name: "multiple resource lists with same resources",
			input: []corev1.ResourceList{
				{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("1Gi"),
				},
				{
					corev1.ResourceCPU:    resource.MustParse("500m"),
					corev1.ResourceMemory: resource.MustParse("512Mi"),
				},
			},
			expected: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1500m"),
				corev1.ResourceMemory: resource.MustParse("1536Mi"),
			},
		},
		{
			name: "multiple resource lists with different resources",
			input: []corev1.ResourceList{
				{
					corev1.ResourceCPU: resource.MustParse("1"),
				},
				{
					corev1.ResourceMemory: resource.MustParse("1Gi"),
				},
				{
					corev1.ResourceEphemeralStorage: resource.MustParse("2Gi"),
				},
			},
			expected: corev1.ResourceList{
				corev1.ResourceCPU:              resource.MustParse("1"),
				corev1.ResourceMemory:           resource.MustParse("1Gi"),
				corev1.ResourceEphemeralStorage: resource.MustParse("2Gi"),
			},
		},
		{
			name: "mix of overlapping and non-overlapping resources",
			input: []corev1.ResourceList{
				{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("1Gi"),
				},
				{
					corev1.ResourceCPU:              resource.MustParse("500m"),
					corev1.ResourceEphemeralStorage: resource.MustParse("1Gi"),
				},
				{
					corev1.ResourceMemory:           resource.MustParse("512Mi"),
					corev1.ResourceEphemeralStorage: resource.MustParse("1Gi"),
					"custom.io/resource":            resource.MustParse("10"),
				},
			},
			expected: corev1.ResourceList{
				corev1.ResourceCPU:              resource.MustParse("1500m"),
				corev1.ResourceMemory:           resource.MustParse("1536Mi"),
				corev1.ResourceEphemeralStorage: resource.MustParse("2Gi"),
				"custom.io/resource":            resource.MustParse("10"),
			},
		},
		{
			name: "nil resource lists",
			input: []corev1.ResourceList{
				nil,
				{
					corev1.ResourceCPU: resource.MustParse("1"),
				},
				nil,
			},
			expected: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("1"),
			},
		},
		{
			name: "empty resource lists",
			input: []corev1.ResourceList{
				{},
				{
					corev1.ResourceCPU: resource.MustParse("1"),
				},
				{},
			},
			expected: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("1"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := combineResourceLists(tt.input...)

			if !resourceListsEqual(tt.expected, result) {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestSetNVCFInfraContainerResources(t *testing.T) {
	tests := []struct {
		name         string
		pod          *corev1.Pod
		expectedPod  *corev1.Pod
		shouldModify bool
	}{
		{
			name: "pod with init container and utils container",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name: common.InitContainerName,
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU: resource.MustParse("100m"),
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name: common.UtilsContainerName,
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU: resource.MustParse("200m"),
								},
							},
						},
					},
				},
			},
			expectedPod: &corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name: common.InitContainerName,
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("4"),
									corev1.ResourceMemory: resource.MustParse("4Gi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("4"),
									corev1.ResourceMemory: resource.MustParse("4Gi"),
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name: common.UtilsContainerName,
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("4"),
									corev1.ResourceMemory: resource.MustParse("4Gi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("4"),
									corev1.ResourceMemory: resource.MustParse("4Gi"),
								},
							},
						},
					},
				},
			},
			shouldModify: true,
		},
		{
			name: "pod without target containers",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name: "other-init",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU: resource.MustParse("100m"),
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name: "other-container",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU: resource.MustParse("200m"),
								},
							},
						},
					},
				},
			},
			expectedPod: &corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name: "other-init",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU: resource.MustParse("100m"),
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name: "other-container",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU: resource.MustParse("200m"),
								},
							},
						},
					},
				},
			},
			shouldModify: false,
		},
		{
			name: "pod with only init container",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name: common.InitContainerName,
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU: resource.MustParse("1"),
								},
							},
						},
					},
				},
			},
			expectedPod: &corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name: common.InitContainerName,
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("4"),
									corev1.ResourceMemory: resource.MustParse("4Gi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("4"),
									corev1.ResourceMemory: resource.MustParse("4Gi"),
								},
							},
						},
					},
				},
			},
			shouldModify: true,
		},
		{
			name: "pod with only utils container",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: common.UtilsContainerName,
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceMemory: resource.MustParse("1Gi"),
								},
							},
						},
					},
				},
			},
			expectedPod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: common.UtilsContainerName,
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("4"),
									corev1.ResourceMemory: resource.MustParse("4Gi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("4"),
									corev1.ResourceMemory: resource.MustParse("4Gi"),
								},
							},
						},
					},
				},
			},
			shouldModify: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset utils resources to defaults for this test
			parseUtilsLimitsOnce = sync.Once{}
			resourcesUtils = corev1.ResourceList{}

			// Create a deep copy of the input pod to compare
			originalPod := tt.pod.DeepCopy()

			SetNVCFInfraContainerResources(defaultUtilsContainerResources.Limits.DeepCopy(), tt.pod)

			if tt.shouldModify {
				// Check if the pod was modified as expected
				assert.Equal(t, tt.expectedPod, tt.pod)
			} else {
				// Verify that only the expected containers were modified
				assert.Equal(t, originalPod, tt.pod)
			}
		})
	}
}

func TestEnsureResourceQuotas(t *testing.T) {
	ctx := core.WithDefaultLogger(t.Context())
	log := core.GetLogger(ctx)
	log.Logger.Level = logrus.DebugLevel
	ctx = core.WithLogger(ctx, log)

	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "sr-foo",
		},
	}
	crClient := clientfake.NewClientBuilder().WithObjects(namespace).Build()

	cs := NewControllerRuntimeClientShim(crClient)

	fff := &featureflagmock.Fetcher{
		EnabledFFs: []*featureflag.FeatureFlag{
			featureflag.HelmResourceConstraints,
		},
	}

	var err error
	one := resource.MustParse("1")
	two := resource.MustParse("2")
	three := resource.MustParse("3")
	_ = three.String()
	six := resource.MustParse("6")
	_ = six.String()

	const (
		resourceNameGPU  corev1.ResourceName = "nvidia.com/gpu"
		resourceNamePGPU corev1.ResourceName = "nvidia.com/pgpu"
	)

	computeReqs := corev1.ResourceList{
		resourceNameGPU:                 resource.MustParse("1"),
		corev1.ResourceCPU:              resource.MustParse("3"),
		corev1.ResourceMemory:           resource.MustParse("32Gi"),
		corev1.ResourceEphemeralStorage: resource.MustParse("128Gi"),
	}
	computeLims := computeReqs.DeepCopy()

	maxObjsRQ := corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "max-objects",
			Namespace: namespace.Name,
		},
		Spec: corev1.ResourceQuotaSpec{
			Hard: corev1.ResourceList{
				"configmaps":              resource.MustParse("20"),
				"secrets":                 resource.MustParse("20"),
				"services":                resource.MustParse("20"),
				"pods":                    resource.MustParse("100"),
				"count/jobs.batch":        resource.MustParse("10"),
				"count/cronjobs.batch":    resource.MustParse("10"),
				"count/deployments.apps":  resource.MustParse("10"),
				"count/replicasets.apps":  resource.MustParse("11"),
				"count/statefulsets.apps": resource.MustParse("11"),
			},
		},
	}

	withResourceVersion := func(rq corev1.ResourceQuota, rv string) corev1.ResourceQuota {
		rqq := rq.DeepCopy()
		rqq.ResourceVersion = rv
		return *rqq
	}

	sortRQs := func(items []corev1.ResourceQuota) []corev1.ResourceQuota {
		sort.Slice(items, func(i, j int) bool {
			return items[i].Name < items[j].Name
		})
		return items
	}
	deleteRQs := func(items []corev1.ResourceQuota) {
		t.Helper()
		for _, rq := range items {
			if err = crClient.Delete(ctx, &rq); err != nil && !errors.IsNotFound(err) {
				require.NoError(t, err)
			}
		}
	}

	// The request should equal instance count.
	err = EnsureResourceQuotas(ctx, fff, cs, common.FunctionCreationAction, namespace.Name, computeReqs, computeLims)
	require.NoError(t, err)
	rqList := &corev1.ResourceQuotaList{}
	err = crClient.List(ctx, rqList)
	if assert.NoError(t, err) && assert.Len(t, sortRQs(rqList.Items), 1) {
		assert.Equal(t, []corev1.ResourceQuota{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "max-gpus",
					Namespace:       namespace.Name,
					ResourceVersion: "1",
				},
				Spec: corev1.ResourceQuotaSpec{
					Hard: corev1.ResourceList{
						"requests.nvidia.com/gpu": one,
						"limits.nvidia.com/gpu":   one,
					},
				},
			},
		}, rqList.Items)
	}

	// Set GPUs to 2, make sure quotas are updated.
	computeReqs2GPU := computeReqs.DeepCopy()
	gpu := computeReqs2GPU[resourceNameGPU]
	require.True(t, gpu.Mul(2))
	computeReqs2GPU[resourceNameGPU] = gpu
	cpu := computeReqs2GPU[corev1.ResourceCPU]
	require.True(t, cpu.Mul(2))
	computeReqs2GPU[corev1.ResourceCPU] = cpu
	memory := computeReqs2GPU[corev1.ResourceMemory]
	require.True(t, memory.Mul(2))
	computeReqs2GPU[corev1.ResourceMemory] = memory
	storage := computeReqs2GPU[corev1.ResourceEphemeralStorage]
	require.True(t, storage.Mul(2))
	computeReqs2GPU[corev1.ResourceEphemeralStorage] = storage

	computeLims2GPU := computeReqs2GPU.DeepCopy()

	err = EnsureResourceQuotas(ctx, fff, cs, common.FunctionCreationAction, namespace.Name, computeReqs2GPU, computeLims2GPU)
	require.NoError(t, err)
	err = crClient.List(ctx, rqList)
	if assert.NoError(t, err) && assert.Len(t, sortRQs(rqList.Items), 1) {
		assert.Equal(t, []corev1.ResourceQuota{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "max-gpus",
					Namespace:       namespace.Name,
					ResourceVersion: "2",
				},
				Spec: corev1.ResourceQuotaSpec{
					Hard: corev1.ResourceList{
						"requests.nvidia.com/gpu": two,
						"limits.nvidia.com/gpu":   two,
					},
				},
			},
		}, rqList.Items)
	}

	// Turn on resource enforcement attribute
	fff.EnabledFFs = []*featureflag.FeatureFlag{
		featureflag.HelmResourceConstraints,
		featureflag.EnforceHelmFunctionResourceLimits,
		featureflag.EnforceHelmTaskResourceLimits,
	}
	deleteRQs(rqList.Items)

	// The task request should equal instance count.
	err = EnsureResourceQuotas(ctx, fff, cs, common.TaskCreationAction, namespace.Name, computeReqs, computeLims)
	require.NoError(t, err)
	err = crClient.List(ctx, rqList)
	if assert.NoError(t, err) && assert.Len(t, sortRQs(rqList.Items), 3) {
		assert.Equal(t, []corev1.ResourceQuota{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "max-gpus",
					Namespace:       namespace.Name,
					ResourceVersion: "1",
				},
				Spec: corev1.ResourceQuotaSpec{
					Hard: corev1.ResourceList{
						"requests.nvidia.com/gpu": one,
						"limits.nvidia.com/gpu":   one,
					},
				},
			},
			withResourceVersion(maxObjsRQ, "1"),
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "max-resources",
					Namespace:       namespace.Name,
					ResourceVersion: "1",
				},
				Spec: corev1.ResourceQuotaSpec{
					Hard: corev1.ResourceList{
						"requests.cpu":               three,
						"requests.memory":            *resource.NewQuantity(32*1<<30, resource.BinarySI),
						"requests.ephemeral-storage": *resource.NewQuantity(128*1<<30, resource.BinarySI),
						"limits.cpu":                 three,
						"limits.memory":              *resource.NewQuantity(32*1<<30, resource.BinarySI),
						"limits.ephemeral-storage":   *resource.NewQuantity(128*1<<30, resource.BinarySI),
					},
				},
			},
		}, rqList.Items)
	}

	// The function request should equal instance count.
	err = EnsureResourceQuotas(ctx, fff, cs, common.FunctionCreationAction, namespace.Name, computeReqs, computeLims)
	require.NoError(t, err)
	err = crClient.List(ctx, rqList)
	if assert.NoError(t, err) && assert.Len(t, sortRQs(rqList.Items), 3) {
		assert.Equal(t, []corev1.ResourceQuota{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "max-gpus",
					Namespace:       namespace.Name,
					ResourceVersion: "1",
				},
				Spec: corev1.ResourceQuotaSpec{
					Hard: corev1.ResourceList{
						"requests.nvidia.com/gpu": one,
						"limits.nvidia.com/gpu":   one,
					},
				},
			},
			withResourceVersion(maxObjsRQ, "1"),
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "max-resources",
					Namespace:       namespace.Name,
					ResourceVersion: "1",
				},
				Spec: corev1.ResourceQuotaSpec{
					Hard: corev1.ResourceList{
						"requests.cpu":               three,
						"requests.memory":            *resource.NewQuantity(32*1<<30, resource.BinarySI),
						"requests.ephemeral-storage": *resource.NewQuantity(128*1<<30, resource.BinarySI),
						"limits.cpu":                 three,
						"limits.memory":              *resource.NewQuantity(32*1<<30, resource.BinarySI),
						"limits.ephemeral-storage":   *resource.NewQuantity(128*1<<30, resource.BinarySI),
					},
				},
			},
		}, rqList.Items)
	}

	// Set GPUs to 2, make sure quotas are updated.
	err = EnsureResourceQuotas(ctx, fff, cs, common.FunctionCreationAction, namespace.Name, computeReqs2GPU, computeLims2GPU)
	require.NoError(t, err)
	err = crClient.List(ctx, rqList)
	if assert.NoError(t, err) && assert.Len(t, sortRQs(rqList.Items), 3) {
		assert.Equal(t, []corev1.ResourceQuota{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "max-gpus",
					Namespace:       namespace.Name,
					ResourceVersion: "2",
				},
				Spec: corev1.ResourceQuotaSpec{
					Hard: corev1.ResourceList{
						"requests.nvidia.com/gpu": two,
						"limits.nvidia.com/gpu":   two,
					},
				},
			},
			withResourceVersion(maxObjsRQ, "1"),
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "max-resources",
					Namespace:       namespace.Name,
					ResourceVersion: "2",
				},
				Spec: corev1.ResourceQuotaSpec{
					Hard: corev1.ResourceList{
						"requests.cpu":               six,
						"requests.memory":            *resource.NewQuantity(64*1<<30, resource.BinarySI),
						"requests.ephemeral-storage": *resource.NewQuantity(256*1<<30, resource.BinarySI),
						"limits.cpu":                 six,
						"limits.memory":              *resource.NewQuantity(64*1<<30, resource.BinarySI),
						"limits.ephemeral-storage":   *resource.NewQuantity(256*1<<30, resource.BinarySI),
					},
				},
			},
		}, rqList.Items)
	}

	// Add kata to attrs, make sure gpu key is updated.
	fff.EnabledFFs = []*featureflag.FeatureFlag{
		featureflag.HelmResourceConstraints,
		featureflag.EnforceHelmFunctionResourceLimits,
	}
	fff.EnabledAttrs = []*featureflag.Attribute{
		featureflag.AttrKataRuntimeIsolation,
	}
	deleteRQs(rqList.Items)

	computeReqs2GPUKata := computeReqs2GPU.DeepCopy()
	delete(computeReqs2GPUKata, resourceNameGPU)
	computeReqs2GPUKata[resourceNamePGPU] = resource.MustParse("2")

	computeLims2GPUKata := computeReqs2GPUKata.DeepCopy()
	err = EnsureResourceQuotas(ctx, fff, cs, common.FunctionCreationAction, namespace.Name, computeReqs2GPUKata, computeLims2GPUKata)
	require.NoError(t, err)
	err = crClient.List(ctx, rqList)
	if assert.NoError(t, err) && assert.Len(t, sortRQs(rqList.Items), 3) {
		assert.Equal(t, []corev1.ResourceQuota{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "max-gpus",
					Namespace:       namespace.Name,
					ResourceVersion: "1",
				},
				Spec: corev1.ResourceQuotaSpec{
					Hard: corev1.ResourceList{
						"requests.nvidia.com/pgpu": two,
						"limits.nvidia.com/pgpu":   two,
					},
				},
			},
			withResourceVersion(maxObjsRQ, "1"),
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "max-resources",
					Namespace:       namespace.Name,
					ResourceVersion: "1",
				},
				Spec: corev1.ResourceQuotaSpec{
					Hard: corev1.ResourceList{
						"requests.cpu":               six,
						"requests.memory":            *resource.NewQuantity(64*1<<30, resource.BinarySI),
						"requests.ephemeral-storage": *resource.NewQuantity(256*1<<30, resource.BinarySI),
						"limits.cpu":                 six,
						"limits.memory":              *resource.NewQuantity(64*1<<30, resource.BinarySI),
						"limits.ephemeral-storage":   *resource.NewQuantity(256*1<<30, resource.BinarySI),
					},
				},
			},
		}, rqList.Items)
	}
}

// Helper functions

func createBase64ResourceList(rl corev1.ResourceList) string {
	bytes, _ := json.Marshal(rl)
	return base64.StdEncoding.EncodeToString(bytes)
}

func resourceListsEqual(a, b corev1.ResourceList) bool {
	if len(a) != len(b) {
		return false
	}
	for key, valueA := range a {
		valueB, exists := b[key]
		if !exists || !valueA.Equal(valueB) {
			return false
		}
	}
	return true
}

func setEnvVar(key, value string) {
	if value == "" {
		os.Unsetenv(key)
	} else {
		os.Setenv(key, value)
	}
}

func restoreEnvVar(key, originalValue string) {
	if originalValue == "" {
		os.Unsetenv(key)
	} else {
		os.Setenv(key, originalValue)
	}
}

func TestGetContainerResourcesBYOO(t *testing.T) {
	t.Run("defaults when no config", func(t *testing.T) {
		cfg := nvcaconfig.Config{}
		resources := GetContainerResourcesBYOO(cfg)

		// Should return hardcoded defaults from vendor library
		expected := common.GetDefaultContainerResourcesBYOO()
		assert.Equal(t, expected, resources)
		assert.Equal(t, "500m", resources.Requests.Cpu().String())
		assert.Equal(t, "2Gi", resources.Requests.Memory().String())
		assert.Equal(t, "500m", resources.Limits.Cpu().String())
		assert.Equal(t, "2Gi", resources.Limits.Memory().String())
	})

	t.Run("uses configured requests", func(t *testing.T) {
		cfg := nvcaconfig.Config{
			Agent: nvcaconfig.AgentConfig{
				BYOOResources: nvcaconfig.ResourceRequirements{
					Requests: nvcaconfig.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
					},
				},
			},
		}
		resources := GetContainerResourcesBYOO(cfg)

		assert.Equal(t, "1", resources.Requests.Cpu().String())
		assert.Equal(t, "1Gi", resources.Requests.Memory().String())
		// Limits should be empty since not configured
		assert.True(t, resources.Limits.Cpu().IsZero())
		assert.True(t, resources.Limits.Memory().IsZero())
	})

	t.Run("uses configured limits", func(t *testing.T) {
		cfg := nvcaconfig.Config{
			Agent: nvcaconfig.AgentConfig{
				BYOOResources: nvcaconfig.ResourceRequirements{
					Limits: nvcaconfig.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("4Gi"),
					},
				},
			},
		}
		resources := GetContainerResourcesBYOO(cfg)

		assert.Equal(t, "2", resources.Limits.Cpu().String())
		assert.Equal(t, "4Gi", resources.Limits.Memory().String())
		// Requests should be empty since not configured
		assert.True(t, resources.Requests.Cpu().IsZero())
		assert.True(t, resources.Requests.Memory().IsZero())
	})

	t.Run("uses configured requests and limits", func(t *testing.T) {
		cfg := nvcaconfig.Config{
			Agent: nvcaconfig.AgentConfig{
				BYOOResources: nvcaconfig.ResourceRequirements{
					Requests: nvcaconfig.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
					},
					Limits: nvcaconfig.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("4Gi"),
					},
				},
			},
		}
		resources := GetContainerResourcesBYOO(cfg)

		assert.Equal(t, "1", resources.Requests.Cpu().String())
		assert.Equal(t, "1Gi", resources.Requests.Memory().String())
		assert.Equal(t, "2", resources.Limits.Cpu().String())
		assert.Equal(t, "4Gi", resources.Limits.Memory().String())
	})
}

func TestSetConfigDefaultResourcesBYOO(t *testing.T) {
	t.Run("sets default BYOO resources", func(t *testing.T) {
		t.Cleanup(func() {
			parseUtilsLimitsOnce = sync.Once{}
			resourcesUtils = corev1.ResourceList{}
			parseAddlOverheadResourcesOnce = sync.Once{}
			addlOverheadResources = corev1.ResourceList{}
		})

		cfg := nvcaconfig.Config{}
		err := SetConfigDefaultResources(&cfg)
		require.NoError(t, err)

		// Check BYOO defaults are set
		reqCPU := cfg.Agent.BYOOResources.Requests[corev1.ResourceCPU]
		reqMem := cfg.Agent.BYOOResources.Requests[corev1.ResourceMemory]
		limCPU := cfg.Agent.BYOOResources.Limits[corev1.ResourceCPU]
		limMem := cfg.Agent.BYOOResources.Limits[corev1.ResourceMemory]
		assert.Equal(t, "500m", reqCPU.String())
		assert.Equal(t, "2Gi", reqMem.String())
		assert.Equal(t, "500m", limCPU.String())
		assert.Equal(t, "2Gi", limMem.String())
	})

	t.Run("preserves configured BYOO resources", func(t *testing.T) {
		t.Cleanup(func() {
			parseUtilsLimitsOnce = sync.Once{}
			resourcesUtils = corev1.ResourceList{}
			parseAddlOverheadResourcesOnce = sync.Once{}
			addlOverheadResources = corev1.ResourceList{}
		})

		cfg := nvcaconfig.Config{
			Agent: nvcaconfig.AgentConfig{
				BYOOResources: nvcaconfig.ResourceRequirements{
					Requests: nvcaconfig.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
					},
					Limits: nvcaconfig.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
					},
				},
			},
		}
		err := SetConfigDefaultResources(&cfg)
		require.NoError(t, err)

		// Check configured values are preserved
		reqCPU := cfg.Agent.BYOOResources.Requests[corev1.ResourceCPU]
		reqMem := cfg.Agent.BYOOResources.Requests[corev1.ResourceMemory]
		limCPU := cfg.Agent.BYOOResources.Limits[corev1.ResourceCPU]
		limMem := cfg.Agent.BYOOResources.Limits[corev1.ResourceMemory]
		assert.Equal(t, "1", reqCPU.String())
		assert.Equal(t, "1Gi", reqMem.String())
		assert.Equal(t, "1", limCPU.String())
		assert.Equal(t, "1Gi", limMem.String())
	})

	t.Run("fills missing BYOO resource types", func(t *testing.T) {
		t.Cleanup(func() {
			parseUtilsLimitsOnce = sync.Once{}
			resourcesUtils = corev1.ResourceList{}
			parseAddlOverheadResourcesOnce = sync.Once{}
			addlOverheadResources = corev1.ResourceList{}
		})

		cfg := nvcaconfig.Config{
			Agent: nvcaconfig.AgentConfig{
				BYOOResources: nvcaconfig.ResourceRequirements{
					Requests: nvcaconfig.ResourceList{
						corev1.ResourceCPU: resource.MustParse("1"),
						// Memory is missing
					},
					Limits: nvcaconfig.ResourceList{
						// CPU is missing
						corev1.ResourceMemory: resource.MustParse("1Gi"),
					},
				},
			},
		}
		err := SetConfigDefaultResources(&cfg)
		require.NoError(t, err)

		// Check configured values are preserved and missing values are filled
		reqCPU := cfg.Agent.BYOOResources.Requests[corev1.ResourceCPU]
		reqMem := cfg.Agent.BYOOResources.Requests[corev1.ResourceMemory]
		limCPU := cfg.Agent.BYOOResources.Limits[corev1.ResourceCPU]
		limMem := cfg.Agent.BYOOResources.Limits[corev1.ResourceMemory]
		assert.Equal(t, "1", reqCPU.String())
		assert.Equal(t, "2Gi", reqMem.String())  // default
		assert.Equal(t, "500m", limCPU.String()) // default
		assert.Equal(t, "1Gi", limMem.String())
	})
}

func TestGetContainerResourcesFluentBit(t *testing.T) {
	t.Run("defaults when no config", func(t *testing.T) {
		cfg := nvcaconfig.Config{}
		resources := GetContainerResourcesFluentBit(cfg)

		// Should return hardcoded defaults from vendor library
		expected := common.GetDefaultContainerResourcesFluentbit()
		assert.Equal(t, expected, resources)
		assert.Equal(t, "100m", resources.Requests.Cpu().String())
		assert.Equal(t, "128Mi", resources.Requests.Memory().String())
		assert.Equal(t, "200m", resources.Limits.Cpu().String())
		assert.Equal(t, "256Mi", resources.Limits.Memory().String())
	})

	t.Run("uses configured requests", func(t *testing.T) {
		cfg := nvcaconfig.Config{
			Agent: nvcaconfig.AgentConfig{
				BYOOFluentBitResources: nvcaconfig.ResourceRequirements{
					Requests: nvcaconfig.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("250m"),
						corev1.ResourceMemory: resource.MustParse("512Mi"),
					},
				},
			},
		}
		resources := GetContainerResourcesFluentBit(cfg)

		assert.Equal(t, "250m", resources.Requests.Cpu().String())
		assert.Equal(t, "512Mi", resources.Requests.Memory().String())
		// Limits should be empty since not configured
		assert.True(t, resources.Limits.Cpu().IsZero())
		assert.True(t, resources.Limits.Memory().IsZero())
	})

	t.Run("uses configured limits", func(t *testing.T) {
		cfg := nvcaconfig.Config{
			Agent: nvcaconfig.AgentConfig{
				BYOOFluentBitResources: nvcaconfig.ResourceRequirements{
					Limits: nvcaconfig.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
					},
				},
			},
		}
		resources := GetContainerResourcesFluentBit(cfg)

		assert.Equal(t, "1", resources.Limits.Cpu().String())
		assert.Equal(t, "1Gi", resources.Limits.Memory().String())
		// Requests should be empty since not configured
		assert.True(t, resources.Requests.Cpu().IsZero())
		assert.True(t, resources.Requests.Memory().IsZero())
	})

	t.Run("uses configured requests and limits", func(t *testing.T) {
		cfg := nvcaconfig.Config{
			Agent: nvcaconfig.AgentConfig{
				BYOOFluentBitResources: nvcaconfig.ResourceRequirements{
					Requests: nvcaconfig.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("250m"),
						corev1.ResourceMemory: resource.MustParse("512Mi"),
					},
					Limits: nvcaconfig.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
					},
				},
			},
		}
		resources := GetContainerResourcesFluentBit(cfg)

		assert.Equal(t, "250m", resources.Requests.Cpu().String())
		assert.Equal(t, "512Mi", resources.Requests.Memory().String())
		assert.Equal(t, "1", resources.Limits.Cpu().String())
		assert.Equal(t, "1Gi", resources.Limits.Memory().String())
	})
}

func TestSetConfigDefaultResourcesFluentBit(t *testing.T) {
	t.Run("sets default FluentBit resources", func(t *testing.T) {
		t.Cleanup(func() {
			parseUtilsLimitsOnce = sync.Once{}
			resourcesUtils = corev1.ResourceList{}
			parseAddlOverheadResourcesOnce = sync.Once{}
			addlOverheadResources = corev1.ResourceList{}
		})

		cfg := nvcaconfig.Config{}
		err := SetConfigDefaultResources(&cfg)
		require.NoError(t, err)

		// Should have default FluentBit resources set
		assert.NotEmpty(t, cfg.Agent.BYOOFluentBitResources.Requests)
		assert.NotEmpty(t, cfg.Agent.BYOOFluentBitResources.Limits)

		// Check default values
		cpuReq := cfg.Agent.BYOOFluentBitResources.Requests[corev1.ResourceCPU]
		memReq := cfg.Agent.BYOOFluentBitResources.Requests[corev1.ResourceMemory]
		cpuLim := cfg.Agent.BYOOFluentBitResources.Limits[corev1.ResourceCPU]
		memLim := cfg.Agent.BYOOFluentBitResources.Limits[corev1.ResourceMemory]
		assert.Equal(t, "100m", cpuReq.String())
		assert.Equal(t, "128Mi", memReq.String())
		assert.Equal(t, "200m", cpuLim.String())
		assert.Equal(t, "256Mi", memLim.String())
	})

	t.Run("preserves existing FluentBit resources", func(t *testing.T) {
		t.Cleanup(func() {
			parseUtilsLimitsOnce = sync.Once{}
			resourcesUtils = corev1.ResourceList{}
			parseAddlOverheadResourcesOnce = sync.Once{}
			addlOverheadResources = corev1.ResourceList{}
		})

		cfg := nvcaconfig.Config{
			Agent: nvcaconfig.AgentConfig{
				BYOOFluentBitResources: nvcaconfig.ResourceRequirements{
					Requests: nvcaconfig.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("250m"),
						corev1.ResourceMemory: resource.MustParse("512Mi"),
					},
					Limits: nvcaconfig.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
					},
				},
			},
		}
		err := SetConfigDefaultResources(&cfg)
		require.NoError(t, err)

		// Should preserve custom values
		cpuReq := cfg.Agent.BYOOFluentBitResources.Requests[corev1.ResourceCPU]
		memReq := cfg.Agent.BYOOFluentBitResources.Requests[corev1.ResourceMemory]
		cpuLim := cfg.Agent.BYOOFluentBitResources.Limits[corev1.ResourceCPU]
		memLim := cfg.Agent.BYOOFluentBitResources.Limits[corev1.ResourceMemory]
		assert.Equal(t, "250m", cpuReq.String())
		assert.Equal(t, "512Mi", memReq.String())
		assert.Equal(t, "1", cpuLim.String())
		assert.Equal(t, "1Gi", memLim.String())
	})

	t.Run("fills in missing resources", func(t *testing.T) {
		t.Cleanup(func() {
			parseUtilsLimitsOnce = sync.Once{}
			resourcesUtils = corev1.ResourceList{}
			parseAddlOverheadResourcesOnce = sync.Once{}
			addlOverheadResources = corev1.ResourceList{}
		})

		cfg := nvcaconfig.Config{
			Agent: nvcaconfig.AgentConfig{
				BYOOFluentBitResources: nvcaconfig.ResourceRequirements{
					Requests: nvcaconfig.ResourceList{
						corev1.ResourceCPU: resource.MustParse("250m"),
						// Memory missing
					},
				},
			},
		}
		err := SetConfigDefaultResources(&cfg)
		require.NoError(t, err)

		// Should preserve CPU, fill in memory
		cpuReq := cfg.Agent.BYOOFluentBitResources.Requests[corev1.ResourceCPU]
		memReq := cfg.Agent.BYOOFluentBitResources.Requests[corev1.ResourceMemory]
		cpuLim := cfg.Agent.BYOOFluentBitResources.Limits[corev1.ResourceCPU]
		memLim := cfg.Agent.BYOOFluentBitResources.Limits[corev1.ResourceMemory]
		assert.Equal(t, "250m", cpuReq.String())
		assert.Equal(t, "128Mi", memReq.String()) // default
		// Limits should be filled with defaults
		assert.Equal(t, "200m", cpuLim.String())
		assert.Equal(t, "256Mi", memLim.String())
	})
}

func TestGetInfraContainerResourceOverhead(t *testing.T) {
	t.Run("includes BYOO and FluentBit when both feature flags enabled", func(t *testing.T) {
		mockFFF := &mockFeatureFlagChecker{
			flags: map[string]bool{
				featureflag.BYOObservability.Key: true,
				featureflag.BYOOFluentBit.Key:    true,
			},
		}

		cfg := nvcaconfig.Config{
			Agent: nvcaconfig.AgentConfig{
				UtilsResources: nvcaconfig.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("4"),
					corev1.ResourceMemory: resource.MustParse("4Gi"),
				},
				BYOOResources: nvcaconfig.ResourceRequirements{
					Limits: nvcaconfig.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
				},
				BYOOFluentBitResources: nvcaconfig.ResourceRequirements{
					Limits: nvcaconfig.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
				},
				SharedStorage: nvcaconfig.SharedStorageConfig{
					Server: nvcaconfig.SharedStorageServerConfig{
						ContainerResources: nvcaconfig.ResourceRequirements{
							Limits: nvcaconfig.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("500m"),
								corev1.ResourceMemory: resource.MustParse("2Gi"),
							},
						},
					},
				},
			},
		}

		gotOverhead := GetInfraContainerResourceOverhead(cfg, mockFFF)

		// ESS: 250m CPU, 128Mi Memory
		// BYOO OTel: 500m CPU, 2Gi (2048Mi) Memory (included when BYOObservability enabled)
		// BYOO FluentBit: 500m CPU, 2Gi (2048Mi) Memory (included when BYOOFluentBit enabled)
		// Utils: 4000m CPU, 4Gi (4096Mi) Memory
		// SharedStorage: 500m CPU, 2Gi (2048Mi) Memory
		// Total: 5750m CPU, 10368Mi Memory
		assert.Equal(t, "5750m", gotOverhead.Cpu().String())
		assert.Equal(t, "10368Mi", gotOverhead.Memory().String())
	})

	t.Run("excludes BYOO when BYOObservability disabled", func(t *testing.T) {
		mockFFF := &mockFeatureFlagChecker{
			flags: map[string]bool{
				featureflag.BYOObservability.Key: false,
				featureflag.BYOOFluentBit.Key:    true,
			},
		}

		cfg := nvcaconfig.Config{
			Agent: nvcaconfig.AgentConfig{
				UtilsResources: nvcaconfig.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("4"),
					corev1.ResourceMemory: resource.MustParse("4Gi"),
				},
				BYOOResources: nvcaconfig.ResourceRequirements{
					Limits: nvcaconfig.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
				},
				BYOOFluentBitResources: nvcaconfig.ResourceRequirements{
					Limits: nvcaconfig.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
				},
				SharedStorage: nvcaconfig.SharedStorageConfig{
					Server: nvcaconfig.SharedStorageServerConfig{
						ContainerResources: nvcaconfig.ResourceRequirements{
							Limits: nvcaconfig.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("500m"),
								corev1.ResourceMemory: resource.MustParse("2Gi"),
							},
						},
					},
				},
			},
		}

		gotOverhead := GetInfraContainerResourceOverhead(cfg, mockFFF)

		// ESS: 250m CPU, 128Mi Memory
		// BYOO OTel: NOT included (BYOObservability disabled)
		// BYOO FluentBit: 500m CPU, 2Gi (2048Mi) Memory
		// Utils: 4000m CPU, 4Gi (4096Mi) Memory
		// SharedStorage: 500m CPU, 2Gi (2048Mi) Memory
		// Total: 5250m CPU, 8320Mi Memory
		assert.Equal(t, "5250m", gotOverhead.Cpu().String())
		assert.Equal(t, "8320Mi", gotOverhead.Memory().String())
	})

	t.Run("excludes FluentBit when BYOOFluentBit disabled", func(t *testing.T) {
		mockFFF := &mockFeatureFlagChecker{
			flags: map[string]bool{
				featureflag.BYOObservability.Key: true,
				featureflag.BYOOFluentBit.Key:    false,
			},
		}

		cfg := nvcaconfig.Config{
			Agent: nvcaconfig.AgentConfig{
				UtilsResources: nvcaconfig.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("4"),
					corev1.ResourceMemory: resource.MustParse("4Gi"),
				},
				BYOOResources: nvcaconfig.ResourceRequirements{
					Limits: nvcaconfig.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
				},
				BYOOFluentBitResources: nvcaconfig.ResourceRequirements{
					Limits: nvcaconfig.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
				},
				SharedStorage: nvcaconfig.SharedStorageConfig{
					Server: nvcaconfig.SharedStorageServerConfig{
						ContainerResources: nvcaconfig.ResourceRequirements{
							Limits: nvcaconfig.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("500m"),
								corev1.ResourceMemory: resource.MustParse("2Gi"),
							},
						},
					},
				},
			},
		}

		gotOverhead := GetInfraContainerResourceOverhead(cfg, mockFFF)

		// ESS: 250m CPU, 128Mi Memory
		// BYOO OTel: 500m CPU, 2Gi (2048Mi) Memory
		// BYOO FluentBit: NOT included (BYOOFluentBit disabled)
		// Utils: 4000m CPU, 4Gi (4096Mi) Memory
		// SharedStorage: 500m CPU, 2Gi (2048Mi) Memory
		// Total: 5250m CPU, 8320Mi Memory
		assert.Equal(t, "5250m", gotOverhead.Cpu().String())
		assert.Equal(t, "8320Mi", gotOverhead.Memory().String())
	})

	t.Run("excludes both when feature flag checker is nil", func(t *testing.T) {
		cfg := nvcaconfig.Config{
			Agent: nvcaconfig.AgentConfig{
				UtilsResources: nvcaconfig.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("4"),
					corev1.ResourceMemory: resource.MustParse("4Gi"),
				},
				BYOOResources: nvcaconfig.ResourceRequirements{
					Limits: nvcaconfig.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
				},
				BYOOFluentBitResources: nvcaconfig.ResourceRequirements{
					Limits: nvcaconfig.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
				},
				SharedStorage: nvcaconfig.SharedStorageConfig{
					Server: nvcaconfig.SharedStorageServerConfig{
						ContainerResources: nvcaconfig.ResourceRequirements{
							Limits: nvcaconfig.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("500m"),
								corev1.ResourceMemory: resource.MustParse("2Gi"),
							},
						},
					},
				},
			},
		}

		gotOverhead := GetInfraContainerResourceOverhead(cfg, nil)

		// When fff is nil, both BYOO and FluentBit should NOT be included
		// ESS: 250m CPU, 128Mi Memory
		// Utils: 4000m CPU, 4Gi (4096Mi) Memory
		// SharedStorage: 500m CPU, 2Gi (2048Mi) Memory
		// Total: 4750m CPU, 6272Mi Memory
		assert.Equal(t, "4750m", gotOverhead.Cpu().String())
		assert.Equal(t, "6272Mi", gotOverhead.Memory().String())
	})

	t.Run("excludes both when both feature flags disabled", func(t *testing.T) {
		mockFFF := &mockFeatureFlagChecker{
			flags: map[string]bool{
				featureflag.BYOObservability.Key: false,
				featureflag.BYOOFluentBit.Key:    false,
			},
		}

		cfg := nvcaconfig.Config{
			Agent: nvcaconfig.AgentConfig{
				UtilsResources: nvcaconfig.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("4"),
					corev1.ResourceMemory: resource.MustParse("4Gi"),
				},
				BYOOResources: nvcaconfig.ResourceRequirements{
					Limits: nvcaconfig.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
				},
				BYOOFluentBitResources: nvcaconfig.ResourceRequirements{
					Limits: nvcaconfig.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
				},
				SharedStorage: nvcaconfig.SharedStorageConfig{
					Server: nvcaconfig.SharedStorageServerConfig{
						ContainerResources: nvcaconfig.ResourceRequirements{
							Limits: nvcaconfig.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("500m"),
								corev1.ResourceMemory: resource.MustParse("2Gi"),
							},
						},
					},
				},
			},
		}

		gotOverhead := GetInfraContainerResourceOverhead(cfg, mockFFF)

		// Both BYOO and FluentBit should NOT be included
		// ESS: 250m CPU, 128Mi Memory
		// Utils: 4000m CPU, 4Gi (4096Mi) Memory
		// SharedStorage: 500m CPU, 2Gi (2048Mi) Memory
		// Total: 4750m CPU, 6272Mi Memory
		assert.Equal(t, "4750m", gotOverhead.Cpu().String())
		assert.Equal(t, "6272Mi", gotOverhead.Memory().String())
	})
}

// mockFeatureFlagChecker is a mock implementation of featureFlagChecker for testing
type mockFeatureFlagChecker struct {
	flags map[string]bool
}

func (m *mockFeatureFlagChecker) IsFeatureFlagEnabled(flag *featureflag.FeatureFlag) bool {
	if flag == nil {
		return false
	}
	return m.flags[flag.Key]
}
