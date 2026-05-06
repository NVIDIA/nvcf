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

package storage

import (
	"fmt"
	"os"
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/task"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	nvcav1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

func TestNewModelCacheStorageRequest(t *testing.T) {
	funcSR := &nvcav2beta1.ICMSRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-req",
		},
		Spec: nvcav2beta1.ICMSRequestSpec{
			NCAId:  "nca-id",
			Action: common.FunctionCreationAction,
			FunctionDetails: function.Details{
				FunctionID:        "func-id",
				FunctionVersionID: "func-version-id",
			},
		},
	}
	taskSR := &nvcav2beta1.ICMSRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-req",
		},
		Spec: nvcav2beta1.ICMSRequestSpec{
			NCAId:  "nca-id",
			Action: common.TaskCreationAction,
			TaskDetails: task.Details{
				TaskID: "task-id",
			},
		},
	}

	tests := []struct {
		name     string
		req      *nvcav2beta1.ICMSRequest
		fff      featureflag.Fetcher
		expected *nvcav2beta1.StorageRequest
		expError string
	}{
		{
			name: "no encryption function",
			req: &nvcav2beta1.ICMSRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-req",
				},
				Spec: nvcav2beta1.ICMSRequestSpec{
					NCAId:  "nca-id",
					Action: common.FunctionCreationAction,
					FunctionDetails: function.Details{
						FunctionID:        "func-id",
						FunctionVersionID: "func-version-id",
					},
					CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
						FunctionLaunchSpecification: &function.LaunchSpecification{
							CacheLaunchSpecification: &common.CacheLaunchSpecification{
								CacheHandle: "foo",
							},
						},
					},
				},
			},
			fff: &featureflagmock.Fetcher{},
			expected: &nvcav2beta1.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name:        nvcav2beta1.ModelCacheRequest.Name(),
					Labels:      nvcatypes.GetLabelsForRequest(funcSR, &featureflagmock.Fetcher{}),
					Annotations: nvcatypes.GetAnnotationsForRequest(funcSR),
				},
				Spec: nvcav2beta1.StorageRequestSpec{
					Type:        nvcav2beta1.ModelCacheRequest,
					RequestName: "test-req",
					ModelCache: &nvcav2beta1.ModelCacheSpec{
						CacheHandle: "foo",
					},
				},
			},
		},
		{
			name: "no encryption task",
			req: &nvcav2beta1.ICMSRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-req",
				},
				Spec: nvcav2beta1.ICMSRequestSpec{
					NCAId:  "nca-id",
					Action: common.TaskCreationAction,
					TaskDetails: task.Details{
						TaskID: "task-id",
					},
					CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
						TaskLaunchSpecification: &task.LaunchSpecification{
							CacheLaunchSpecification: &common.CacheLaunchSpecification{
								CacheHandle: "foo",
							},
						},
					},
				},
			},
			fff: &featureflagmock.Fetcher{},
			expected: &nvcav2beta1.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name:        nvcav2beta1.ModelCacheRequest.Name(),
					Labels:      nvcatypes.GetLabelsForRequest(taskSR, &featureflagmock.Fetcher{}),
					Annotations: nvcatypes.GetAnnotationsForRequest(taskSR),
				},
				Spec: nvcav2beta1.StorageRequestSpec{
					Type:        nvcav2beta1.ModelCacheRequest,
					RequestName: "test-req",
					ModelCache: &nvcav2beta1.ModelCacheSpec{
						CacheHandle: "foo",
					},
				},
			},
		},
		{
			name: "with encryption function",
			req: &nvcav2beta1.ICMSRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-req",
				},
				Spec: nvcav2beta1.ICMSRequestSpec{
					NCAId:  "nca-id",
					Action: common.FunctionCreationAction,
					FunctionDetails: function.Details{
						FunctionID:        "func-id",
						FunctionVersionID: "func-version-id",
					},
					CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
						FunctionLaunchSpecification: &function.LaunchSpecification{
							CacheLaunchSpecification: &common.CacheLaunchSpecification{
								CacheHandle: "foo",
							},
						},
					},
				},
			},
			fff: &featureflagmock.Fetcher{
				EnabledFFs: []*featureflag.FeatureFlag{
					featureflag.NVMeshEncryption,
				},
			},
			expected: &nvcav2beta1.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name:        nvcav2beta1.ModelCacheRequest.Name(),
					Labels:      nvcatypes.GetLabelsForRequest(funcSR, &featureflagmock.Fetcher{}),
					Annotations: nvcatypes.GetAnnotationsForRequest(funcSR),
				},
				Spec: nvcav2beta1.StorageRequestSpec{
					Type:        nvcav2beta1.ModelCacheRequest,
					RequestName: "test-req",
					ModelCache: &nvcav2beta1.ModelCacheSpec{
						CacheHandle: "foo",
						Encryption: &nvcav2beta1.ModelCacheEncryption{
							Required: true,
						},
					},
				},
			},
		},
		{
			name: "with encryption task",
			req: &nvcav2beta1.ICMSRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-req",
				},
				Spec: nvcav2beta1.ICMSRequestSpec{
					NCAId:  "nca-id",
					Action: common.TaskCreationAction,
					TaskDetails: task.Details{
						TaskID: "task-id",
					},
					CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
						TaskLaunchSpecification: &task.LaunchSpecification{
							CacheLaunchSpecification: &common.CacheLaunchSpecification{
								CacheHandle: "foo",
							},
						},
					},
				},
			},
			fff: &featureflagmock.Fetcher{
				EnabledFFs: []*featureflag.FeatureFlag{
					featureflag.NVMeshEncryption,
				},
			},
			expected: &nvcav2beta1.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name:        nvcav2beta1.ModelCacheRequest.Name(),
					Labels:      nvcatypes.GetLabelsForRequest(taskSR, &featureflagmock.Fetcher{}),
					Annotations: nvcatypes.GetAnnotationsForRequest(taskSR),
				},
				Spec: nvcav2beta1.StorageRequestSpec{
					Type:        nvcav2beta1.ModelCacheRequest,
					RequestName: "test-req",
					ModelCache: &nvcav2beta1.ModelCacheSpec{
						CacheHandle: "foo",
						Encryption: &nvcav2beta1.ModelCacheEncryption{
							Required: true,
						},
					},
				},
			},
		},
		{
			name: "with no launch spec",
			req: &nvcav2beta1.ICMSRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-req",
				},
				Spec: nvcav2beta1.ICMSRequestSpec{
					NCAId:  "nca-id",
					Action: common.TaskCreationAction,
					TaskDetails: task.Details{
						TaskID: "task-id",
					},
					CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
						TaskLaunchSpecification: &task.LaunchSpecification{},
					},
				},
			},
			fff:      &featureflagmock.Fetcher{},
			expError: "cache launch specification is not set",
		},
		{
			name: "with no cache handle",
			req: &nvcav2beta1.ICMSRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-req",
				},
				Spec: nvcav2beta1.ICMSRequestSpec{
					NCAId:  "nca-id",
					Action: common.TaskCreationAction,
					TaskDetails: task.Details{
						TaskID: "task-id",
					},
					CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
						TaskLaunchSpecification: &task.LaunchSpecification{
							CacheLaunchSpecification: &common.CacheLaunchSpecification{},
						},
					},
				},
			},
			fff:      &featureflagmock.Fetcher{},
			expError: "cache handle is not set",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual, err := NewModelCacheStorageRequest(tt.req, tt.fff)
			if tt.expError != "" {
				assert.EqualError(t, err, tt.expError)
			} else {
				assert.Equal(t, tt.expected, actual)
			}
		})
	}
}

func TestNewSharedStorageRequest(t *testing.T) {
	funcSR := &nvcav2beta1.ICMSRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-req",
		},
		Spec: nvcav2beta1.ICMSRequestSpec{
			NCAId: "nca-id",
			FunctionDetails: function.Details{
				FunctionID:        "func-id",
				FunctionVersionID: "func-version-id",
			},
			CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
				CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
					InstanceTypeValue: "AZURE.GPU.TESLA",
				},
			},
		},
	}
	taskSR := &nvcav2beta1.ICMSRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-req",
		},
		Spec: nvcav2beta1.ICMSRequestSpec{
			NCAId: "nca-id",
			TaskDetails: task.Details{
				TaskID: "task-id",
			},
		},
	}

	tests := []struct {
		name       string
		req        *nvcav2beta1.ICMSRequest
		encryption bool
		fff        featureflag.Fetcher
		expected   *nvcav2beta1.StorageRequest
	}{
		{
			name:       "function",
			req:        funcSR,
			encryption: false,
			fff:        &featureflagmock.Fetcher{},
			expected: &nvcav2beta1.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name:        nvcav2beta1.SharedStorageRequest.Name(),
					Labels:      nvcatypes.GetLabelsForRequest(funcSR, &featureflagmock.Fetcher{}),
					Annotations: nvcatypes.GetAnnotationsForRequest(funcSR),
				},
				Spec: nvcav2beta1.StorageRequestSpec{
					Type:        nvcav2beta1.SharedStorageRequest,
					RequestName: "test-req",
					SharedStorage: &nvcav2beta1.SharedStorageSpec{
						SMBContainerImage:     "smb:latest",
						WorkerPullSecretName:  "foo-worker",
						WorkerPullSecretNames: []string{"foo-worker"},
						Size:                  *resource.NewQuantity(1<<20, resource.BinarySI),
						Server: &nvcav2beta1.SharedStorageServerSpec{
							SMBServerPodNodeAffinity: &corev1.Affinity{
								NodeAffinity: &corev1.NodeAffinity{
									RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
										NodeSelectorTerms: []corev1.NodeSelectorTerm{
											{
												MatchExpressions: []corev1.NodeSelectorRequirement{
													{
														Key:      nodefeatures.UniformInstanceTypeLabelKey,
														Operator: corev1.NodeSelectorOpIn,
														Values:   []string{"AZURE.GPU.TESLA"},
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "task",
			req:  taskSR,
			fff:  &featureflagmock.Fetcher{},
			expected: &nvcav2beta1.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name:        nvcav2beta1.SharedStorageRequest.Name(),
					Labels:      nvcatypes.GetLabelsForRequest(taskSR, &featureflagmock.Fetcher{}),
					Annotations: nvcatypes.GetAnnotationsForRequest(taskSR),
				},
				Spec: nvcav2beta1.StorageRequestSpec{
					Type:        nvcav2beta1.SharedStorageRequest,
					RequestName: "test-req",
					SharedStorage: &nvcav2beta1.SharedStorageSpec{
						SMBContainerImage:     "smb:latest",
						WorkerPullSecretName:  "foo-worker",
						WorkerPullSecretNames: []string{"foo-worker"},
						Size:                  *resource.NewQuantity(1<<20, resource.BinarySI),
					},
				},
			},
		},
		{
			name: "task with GXCache enabled adds skip injection annotation",
			req:  taskSR,
			fff: &featureflagmock.Fetcher{
				EnabledFFs: []*featureflag.FeatureFlag{
					featureflag.GXCache,
				},
			},
			expected: &nvcav2beta1.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name: nvcav2beta1.SharedStorageRequest.Name(),
					Labels: nvcatypes.GetLabelsForRequest(taskSR, &featureflagmock.Fetcher{
						EnabledFFs: []*featureflag.FeatureFlag{featureflag.GXCache},
					}),
					Annotations: func() map[string]string {
						ann := nvcatypes.GetAnnotationsForRequest(taskSR)
						if ann == nil {
							ann = map[string]string{}
						}
						ann[nvcatypes.GXCacheSkipInjectionAnnotationKey] = nvcatypes.GXCacheSkipInjectionAnnotationValue
						return ann
					}(),
				},
				Spec: nvcav2beta1.StorageRequestSpec{
					Type:        nvcav2beta1.SharedStorageRequest,
					RequestName: "test-req",
					SharedStorage: &nvcav2beta1.SharedStorageSpec{
						SMBContainerImage:     "smb:latest",
						WorkerPullSecretName:  "foo-worker",
						WorkerPullSecretNames: []string{"foo-worker"},
						Size:                  *resource.NewQuantity(1<<20, resource.BinarySI),
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := nvcaconfig.Config{}
			cfg.Agent.SharedStorage.Server.Image = "smb:latest"
			err := k8sutil.SetConfigDefaultResources(&cfg)
			require.NoError(t, err)
			actual := NewSharedStorageRequest(tt.req, tt.fff, cfg, []*corev1.Secret{
				{ObjectMeta: metav1.ObjectMeta{Name: "foo-worker"}},
			})
			assert.Equal(t, tt.expected, actual)
		})
	}
}

func TestNewSharedStorageRequest_AppliesWorkloadTolerations(t *testing.T) {
	req := &nvcav2beta1.ICMSRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-req",
		},
	}
	cfg := nvcaconfig.Config{}
	cfg.Agent.SharedStorage.Server.Image = "smb:latest"
	cfg.Workload.Tolerations = []corev1.Toleration{{
		Key:      "nvidia.com/test-workload",
		Operator: corev1.TolerationOpExists,
		Effect:   corev1.TaintEffectNoSchedule,
	}}
	err := k8sutil.SetConfigDefaultResources(&cfg)
	require.NoError(t, err)

	actual := NewSharedStorageRequest(req, &featureflagmock.Fetcher{}, cfg, []*corev1.Secret{
		{ObjectMeta: metav1.ObjectMeta{Name: "foo-worker"}},
	})

	require.NotNil(t, actual.Spec.SharedStorage)
	require.NotNil(t, actual.Spec.SharedStorage.Server)
	assert.Equal(t, cfg.Workload.Tolerations, actual.Spec.SharedStorage.Server.SMBServerPodTolerations)
}

func TestNewInternalPersistentStorageRequest(t *testing.T) {
	tests := []struct {
		name     string
		req      *nvcav2beta1.ICMSRequest
		intSpec  featureflag.InternalPersistentStorageSpec
		fff      featureflag.Fetcher
		expected *nvcav2beta1.StorageRequest
	}{
		{
			name: "basic",
			req: &nvcav2beta1.ICMSRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-req",
				},
			},
			intSpec: featureflag.InternalPersistentStorageSpec{
				StorageClassName: "test-sc",
				ResourceQuota: nvcav1.InternalPersistentStorageResourceQuotaSpec{
					Hard: map[corev1.ResourceName]resource.Quantity{
						corev1.ResourceRequestsStorage: resource.MustParse("1Gi"),
					},
				},
			},
			fff: &mockFetcher{},
			expected: &nvcav2beta1.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name:        nvcav2beta1.InternalPersistentStorageRequest.Name(),
					Labels:      nvcatypes.GetLabelsForRequest(&nvcav2beta1.ICMSRequest{ObjectMeta: metav1.ObjectMeta{Name: "test-req"}}, &mockFetcher{}),
					Annotations: nvcatypes.GetAnnotationsForRequest(&nvcav2beta1.ICMSRequest{ObjectMeta: metav1.ObjectMeta{Name: "test-req"}}),
				},
				Spec: nvcav2beta1.StorageRequestSpec{
					Type:        nvcav2beta1.InternalPersistentStorageRequest,
					RequestName: "test-req",
					InternalPersistentStorage: &nvcav2beta1.InternalPersistentStorageSpec{
						StorageClassName: "test-sc",
						ResourceQuota: nvcav2beta1.InternalPersistentStorageResourceQuotaSpec{
							Hard: map[corev1.ResourceName]resource.Quantity{
								corev1.ResourceRequestsStorage: resource.MustParse("1Gi"),
							},
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := NewInternalPersistentStorageRequest(tt.req, tt.intSpec, tt.fff)
			assert.Equal(t, tt.expected, actual)
		})
	}
}

func Test_getStorageRequestNameFromType(t *testing.T) {
	tests := []struct {
		input    nvcav1.StorageRequestType
		expected string
	}{
		{input: nvcav1.SharedStorageRequest, expected: nvcav1.SharedStorageRequest.Name()},
		{input: nvcav1.ModelCacheRequest, expected: nvcav1.ModelCacheRequest.Name()},
		{input: nvcav1.InternalPersistentStorageRequest, expected: nvcav1.InternalPersistentStorageRequest.Name()},
		{input: nvcav1.StorageRequestType("not-a-type"), expected: ""},
	}

	for _, test := range tests {
		t.Run(fmt.Sprintf("input: %s", test.input), func(t *testing.T) {
			assert.Equal(t, test.expected, test.input.Name())
		})
	}
}

func TestGetSharedStorageServerImage(t *testing.T) {
	tests := []struct {
		name           string
		icmsServiceURL string
		envVarSet      bool
		envVarValue    string
		envCfgValue    nvcaconfig.Environment
		expectedImage  string
	}{
		{
			name:           "default image",
			icmsServiceURL: "https://example.com",
			envVarSet:      false,
			expectedImage:  "nvcr.io/qtfpt1h0bieu/nvcf-core/samba:1.0.5",
		},
		{
			name:           "env var override",
			icmsServiceURL: "https://example.com",
			envVarSet:      true,
			envVarValue:    "custom-image",
			expectedImage:  "custom-image",
		},
		{
			name:           "stg. URL",
			icmsServiceURL: "https://stg.example.com",
			envVarSet:      false,
			expectedImage:  "stg.nvcr.io/nv-cf/nvcf-core/samba:1.0.5",
		},
		{
			name:           "stg cfg",
			icmsServiceURL: "https://example.com",
			envVarSet:      false,
			envCfgValue:    nvcaconfig.EnvironmentStaging,
			expectedImage:  "stg.nvcr.io/nv-cf/nvcf-core/samba:1.0.5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envVarSet {
				os.Setenv("NVCA_SHARED_STORAGE_IMAGE", tt.envVarValue)
				defer os.Unsetenv("NVCA_SHARED_STORAGE_IMAGE")
			}
			actualImage := GetSharedStorageServerImage(tt.icmsServiceURL, tt.envCfgValue)
			assert.Equal(t, tt.expectedImage, actualImage)
		})
	}
}

func TestIsRequeableStorageError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "non-requeable error",
			err:      fmt.Errorf("some error"),
			expected: false,
		},
		{
			name:     "requeable error",
			err:      NewRequeueableStorageError("some error"),
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := IsRequeableStorageError(tt.err)
			assert.Equal(t, tt.expected, actual)
		})
	}
}

func TestNewRequeueableStorageError(t *testing.T) {
	tests := []struct {
		name     string
		message  string
		expected string
	}{
		{
			name:     "simple message",
			message:  "some error",
			expected: "some error - RequeableStorageError",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := NewRequeueableStorageError(tt.message).Error()
			assert.Equal(t, tt.expected, actual)
		})
	}
}

func TestGetStorageRequestErrorLog(t *testing.T) {
	tests := []struct {
		name          string
		storageReq    *nvcav2beta1.StorageRequest
		expectedError string
	}{
		{
			name: "failed phase with SMB CSI driver not installed",
			storageReq: &nvcav2beta1.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-storage",
				},
				Status: nvcav2beta1.StorageRequestStatus{
					Phase: nvcav2beta1.StorageFailed,
					Conditions: []metav1.Condition{
						{
							Type:   ConditionTypeSMBCSIDriverInstalled,
							Status: metav1.ConditionFalse,
						},
					},
				},
			},
			expectedError: fmt.Sprintf("%s driver must be installed when %s feature flag is enabled. Please contact your cluster administrator to install the driver.", SMBCSIDriverName, featureflag.HelmSharedStorage.Key),
		},
		{
			name: "failed phase without SMB CSI driver condition",
			storageReq: &nvcav2beta1.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-storage",
				},
				Status: nvcav2beta1.StorageRequestStatus{
					Phase: nvcav2beta1.StorageFailed,
				},
			},
			expectedError: "storage request test-storage is in Failed phase",
		},
		{
			name: "runtime error phase",
			storageReq: &nvcav2beta1.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-storage",
				},
				Status: nvcav2beta1.StorageRequestStatus{
					Phase: nvcav2beta1.StorageRuntimeError,
				},
			},
			expectedError: "storage request test-storage is in RuntimeError phase",
		},
		{
			name: "healthy phase",
			storageReq: &nvcav2beta1.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-storage",
				},
				Status: nvcav2beta1.StorageRequestStatus{
					Phase: nvcav2beta1.StorageReady,
				},
			},
			expectedError: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errorLog := GetStorageRequestErrorLog(tt.storageReq)
			assert.Equal(t, tt.expectedError, errorLog)
		})
	}
}

type mockFetcher struct {
}

func (*mockFetcher) IsFeatureFlagEnabled(*featureflag.FeatureFlag) bool {
	return false
}

func (*mockFetcher) IsAttributeEnabled(*featureflag.Attribute) bool {
	return false
}
