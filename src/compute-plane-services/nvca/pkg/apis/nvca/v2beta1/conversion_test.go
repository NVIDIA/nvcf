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

	corev1 "k8s.io/api/core/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStorageRequestRoundTripPreservesSMBServerPodTolerations(t *testing.T) {
	original := &StorageRequest{
		Spec: StorageRequestSpec{
			SharedStorage: &SharedStorageSpec{
				Server: &SharedStorageServerSpec{
					SMBServerPodTolerations: []corev1.Toleration{{
						Key:      "nvidia.com/test-workload",
						Operator: corev1.TolerationOpExists,
						Effect:   corev1.TaintEffectNoSchedule,
					}},
				},
			},
		},
	}

	convertedToV1 := StorageRequestToV1(original)
	require.NotNil(t, convertedToV1.Spec.SharedStorage)
	require.NotNil(t, convertedToV1.Spec.SharedStorage.Server)

	roundTripped := StorageRequestFromV1(convertedToV1)
	require.NotNil(t, roundTripped.Spec.SharedStorage)
	require.NotNil(t, roundTripped.Spec.SharedStorage.Server)
	assert.Equal(t,
		original.Spec.SharedStorage.Server.SMBServerPodTolerations,
		roundTripped.Spec.SharedStorage.Server.SMBServerPodTolerations,
	)
}

func TestStorageRequestRoundTripPreservesModelCacheBackend(t *testing.T) {
	original := &StorageRequest{
		Spec: StorageRequestSpec{
			ModelCache: &ModelCacheSpec{
				CacheHandle: "handle-1",
				Backend:     "sharedfs",
			},
		},
	}

	toV1 := StorageRequestToV1(original)
	require.NotNil(t, toV1.Spec.ModelCache)
	assert.Equal(t, "sharedfs", toV1.Spec.ModelCache.Backend)

	roundTripped := StorageRequestFromV1(toV1)
	require.NotNil(t, roundTripped.Spec.ModelCache)
	assert.Equal(t, "handle-1", roundTripped.Spec.ModelCache.CacheHandle)
	assert.Equal(t, "sharedfs", roundTripped.Spec.ModelCache.Backend)
}
