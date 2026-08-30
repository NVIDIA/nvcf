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

package nvca

import (
	"context"
	"errors"
	"fmt"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"

	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	nvcastorage "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
)

func cacheSelectionInput(
	req *nvcav2beta1.ICMSRequest,
) (*common.CacheLaunchSpecification, nvcastorage.ModelCacheWorkflow) {
	switch {
	case req.Spec.CreationMsgInfo.FunctionLaunchSpecification != nil:
		spec := req.Spec.CreationMsgInfo.FunctionLaunchSpecification
		workflow := nvcastorage.ModelCacheWorkflowRegular
		if spec.HelmChartLaunchSpecification != nil {
			workflow = nvcastorage.ModelCacheWorkflowHelm
		}
		return spec.CacheLaunchSpecification, workflow
	case req.Spec.CreationMsgInfo.TaskLaunchSpecification != nil:
		spec := req.Spec.CreationMsgInfo.TaskLaunchSpecification
		workflow := nvcastorage.ModelCacheWorkflowRegular
		if spec.HelmChartLaunchSpecification != nil {
			workflow = nvcastorage.ModelCacheWorkflowHelm
		}
		return spec.CacheLaunchSpecification, workflow
	default:
		return nil, ""
	}
}

// persistModelCacheStorageSelection resolves and records the cache decision
// before the ICMSRequest is created. An absent annotation therefore identifies
// a request created by legacy NVCA.
func (c *BackendK8sCache) persistModelCacheStorageSelection(
	ctx context.Context,
	req *nvcav2beta1.ICMSRequest,
) error {
	cacheSpec, workflow := cacheSelectionInput(req)
	if cacheSpec == nil || cacheSpec.CacheSize <= 0 {
		return nil
	}

	mode := nvcastorage.ModelCacheSelectionNone
	var resolved *nvcastorage.ModelCacheStorageSelection
	cachingEnabled := c.featureFlagFetcher.IsFeatureFlagEnabled(featureflag.CachingSupport)
	if workflow == nvcastorage.ModelCacheWorkflowHelm {
		cachingEnabled = cachingEnabled &&
			c.featureFlagFetcher.IsFeatureFlagEnabled(featureflag.HelmModelCaching)
	}
	if cachingEnabled {
		var err error
		resolved, err = nvcastorage.ResolveModelCacheStorageWithClientset(
			ctx, c.clients.K8s, c.systemNamespace, workflow)
		switch {
		case errors.Is(err, nvcastorage.ErrModelCacheStorageClassNotFound):
			if workflow == nvcastorage.ModelCacheWorkflowHelm {
				mode = nvcastorage.ModelCacheSelectionEphemeral
			}
		case err != nil:
			return fmt.Errorf("resolve model cache storage: %w", err)
		case resolved.Transition == nvcastorage.ModelCacheTransitionDisabled:
			if workflow == nvcastorage.ModelCacheWorkflowHelm {
				mode = nvcastorage.ModelCacheSelectionEphemeral
			}
		case resolved.Transition == nvcastorage.ModelCacheTransitionNVMesh:
			mode = nvcastorage.ModelCacheSelectionDurable
		default:
			return fmt.Errorf("unsupported model cache transition %q", resolved.Transition)
		}
	}

	selection, err := nvcastorage.NewPersistedModelCacheStorageSelection(workflow, mode, resolved)
	if err != nil {
		return fmt.Errorf("build model cache storage selection: %w", err)
	}
	if selection.Mode == nvcastorage.ModelCacheSelectionDurable {
		selection.EncryptionRequired = c.featureFlagFetcher.IsFeatureFlagEnabled(featureflag.NVMeshEncryption)
	}
	payload, err := selection.Marshal()
	if err != nil {
		return err
	}
	if req.Annotations == nil {
		req.Annotations = map[string]string{}
	}
	req.Annotations[nvcastorage.ModelCacheStorageSelectionAnnotationKey] = payload
	return nil
}
