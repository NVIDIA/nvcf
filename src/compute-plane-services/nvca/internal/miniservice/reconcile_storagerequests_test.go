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

package mscontroller

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"

	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
	nvcastorage "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
)

// The cache backend is selected ONCE per reconcile by the caller (doInstall)
// and passed into makeStorageRequests, so the ModelCacheRequest decision and
// the ephemeral-annotation decision can never disagree (no second StorageClass
// lookup to race). These tests cover makeStorageRequests's contract given a
// backend: tag the ModelCacheRequest with it, keep invalid cache specs
// terminal, and emit no ModelCacheRequest for the ephemeral backend.
func TestMakeStorageRequests_BackendHandling(t *testing.T) {
	r := &Reconciler{
		ControllerOptions: ControllerOptions{
			FeatureFlagFetcher: &featureflagmock.Fetcher{},
		},
	}
	job := &batchv1.Job{}
	pvc := &corev1.PersistentVolumeClaim{}

	t.Run("samba backend tags the ModelCacheRequest", func(t *testing.T) {
		icmsReq := &nvcav2beta1.ICMSRequest{}
		icmsReq.Spec.Action = common.FunctionCreationAction
		icmsReq.Spec.CreationMsgInfo.FunctionLaunchSpecification = &function.LaunchSpecification{
			CacheLaunchSpecification: &common.CacheLaunchSpecification{
				CacheArtifacts: true,
				CacheHandle:    "h1",
				CacheSize:      262144000,
			},
		}
		sts, err := r.makeStorageRequests(icmsReq, nil, job, pvc, nvcastorage.HelmCacheBackendSamba)
		require.NoError(t, err)
		require.Len(t, sts, 1)
		require.NotNil(t, sts[0].Spec.ModelCache)
		assert.Equal(t, string(nvcastorage.HelmCacheBackendSamba), sts[0].Spec.ModelCache.Backend)
	})

	t.Run("invalid cache spec is terminal", func(t *testing.T) {
		icmsReq := &nvcav2beta1.ICMSRequest{}
		icmsReq.Spec.Action = common.FunctionCreationAction
		// Requests caching (size > 0) but is invalid: no cache handle.
		icmsReq.Spec.CreationMsgInfo.FunctionLaunchSpecification = &function.LaunchSpecification{
			CacheLaunchSpecification: &common.CacheLaunchSpecification{
				CacheArtifacts: true,
				CacheSize:      262144000,
			},
		}
		_, err := r.makeStorageRequests(icmsReq, nil, job, pvc, nvcastorage.HelmCacheBackendSamba)
		require.Error(t, err)
		assert.True(t, errors.Is(err, reconcile.TerminalError(nil)),
			"invalid cache spec must be terminal, not retried")
	})

	t.Run("cache spec without a requested size emits no ModelCacheRequest", func(t *testing.T) {
		icmsReq := &nvcav2beta1.ICMSRequest{}
		icmsReq.Spec.Action = common.FunctionCreationAction
		icmsReq.Spec.CreationMsgInfo.FunctionLaunchSpecification = &function.LaunchSpecification{
			CacheLaunchSpecification: &common.CacheLaunchSpecification{
				CacheArtifacts: true,
				CacheHandle:    "h1",
			},
		}
		sts, err := r.makeStorageRequests(icmsReq, nil, job, pvc, nvcastorage.HelmCacheBackendSamba)
		require.NoError(t, err)
		assert.Empty(t, sts, "cacheLaunchRequested gates durable backends like the ephemeral path")
	})

	t.Run("ephemeral backend emits no ModelCacheRequest", func(t *testing.T) {
		icmsReq := &nvcav2beta1.ICMSRequest{}
		icmsReq.Spec.Action = common.FunctionCreationAction
		sts, err := r.makeStorageRequests(icmsReq, nil, job, pvc, nvcastorage.HelmCacheBackendEphemeral)
		require.NoError(t, err)
		assert.Empty(t, sts)
	})
}
